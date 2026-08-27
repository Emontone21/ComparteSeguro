// Package servidor arma la aplicación HTTP: rutas, middlewares y páginas.
package servidor

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/Emontone21/ComparteSeguro/internal/almacen"
	"github.com/Emontone21/ComparteSeguro/internal/ratelimit"
	"github.com/Emontone21/ComparteSeguro/web"
)

const (
	// largoIDBytes es el tamaño del identificador en bytes crudos. 18 bytes son
	// 144 bits de entropía, por encima de los 128 que se pidieron, y además
	// caen justo en un múltiplo de 3, así que en base64 quedan 24 caracteres
	// sin relleno.
	largoIDBytes = 18
	largoIDTexto = 24

	// sobrecargaCifrado es lo que AES-GCM le suma al texto en claro: 12 bytes
	// de vector de inicialización más 16 de etiqueta de autenticación.
	sobrecargaCifrado = 12 + 16

	// mensajeNotaAusente es la única respuesta que da el servidor cuando no
	// entrega una nota. Es deliberadamente la misma para una nota ya leída, una
	// que nunca existió y un identificador mal formado: si fueran distintas,
	// quien probara identificadores al azar podría averiguar cuáles llegaron a
	// existir alguna vez.
	mensajeNotaAusente = "Esta nota ya fue leída o no existe"
)

// Opciones reúne las dependencias del servidor.
type Opciones struct {
	Almacen             *almacen.Almacen
	Log                 *slog.Logger
	MaxBytesNota        int
	PeticionesPorMinuto float64
	Rafaga              int
	ConfiarEnProxy      bool

	// Organizacion y Ubicacion se muestran en la cabecera.
	Organizacion string
	Ubicacion    string
	// DirectorioMarca es la carpeta del operador con el logo y las fotos
	// oficiales. Vacía significa usar lo que viene en el binario.
	DirectorioMarca string

	// Reloj permite fijar el tiempo en las pruebas. Si es nil, se usa time.Now.
	Reloj func() time.Time
}

// Servidor es la aplicación completa. Implementa http.Handler.
type Servidor struct {
	almacen        *almacen.Almacen
	log            *slog.Logger
	limitador      *ratelimit.Limitador
	maxBytesNota   int
	maxBytesCuerpo int64
	confiarEnProxy bool
	reloj          func() time.Time

	marca         Marca
	recursosMarca map[string]recursoDeMarca
	hojaMarca     []byte

	paginaInicio []byte
	paginaNota   []byte

	manejador http.Handler
}

// datosDePagina es lo que reciben las plantillas de las dos páginas.
type datosDePagina struct {
	MaxBytesNota int
	Marca        Marca
}

// Nuevo construye el servidor y deja las rutas listas.
func Nuevo(o Opciones) (*Servidor, error) {
	if o.Almacen == nil {
		return nil, fmt.Errorf("servidor: falta el almacén")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.MaxBytesNota < 1 {
		return nil, fmt.Errorf("servidor: MaxBytesNota debe ser positivo")
	}
	if o.Reloj == nil {
		o.Reloj = time.Now
	}

	s := &Servidor{
		almacen:        o.Almacen,
		log:            o.Log,
		limitador:      ratelimit.Nuevo(o.PeticionesPorMinuto, o.Rafaga),
		maxBytesNota:   o.MaxBytesNota,
		maxBytesCuerpo: limiteDeCuerpo(o.MaxBytesNota),
		confiarEnProxy: o.ConfiarEnProxy,
		reloj:          o.Reloj,
	}

	marca, recursos, err := resolverMarca(o.DirectorioMarca, o.Organizacion, o.Ubicacion, o.Log)
	if err != nil {
		return nil, err
	}
	s.marca, s.recursosMarca, s.hojaMarca = marca, recursos, hojaDeMarca(marca)

	if err := s.prepararPaginas(); err != nil {
		return nil, err
	}
	if err := s.prepararRutas(); err != nil {
		return nil, err
	}
	return s, nil
}

// limiteDeCuerpo calcula cuánto puede pesar como máximo el cuerpo de una
// petición de creación: el texto en claro, más lo que agrega el cifrado, más lo
// que agrega codificarlo en base64, más un margen para el envoltorio JSON.
func limiteDeCuerpo(maxBytesNota int) int64 {
	crudo := maxBytesNota + sobrecargaCifrado
	enBase64 := (crudo + 2) / 3 * 4
	const margenJSON = 512
	return int64(enBase64 + margenJSON)
}

// prepararPaginas renderiza una sola vez las dos páginas embebidas.
//
// La de inicio lleva inyectado el límite de tamaño para que el contador del
// navegador y la validación del servidor no puedan quedar desincronizados, y
// las dos llevan la marca resuelta al arrancar.
func (s *Servidor) prepararPaginas() error {
	datos := datosDePagina{MaxBytesNota: s.maxBytesNota, Marca: s.marca}

	var err error
	if s.paginaInicio, err = renderizar("index.html", datos); err != nil {
		return err
	}
	if s.paginaNota, err = renderizar("nota.html", datos); err != nil {
		return err
	}
	return nil
}

func renderizar(nombre string, datos datosDePagina) ([]byte, error) {
	crudo, err := web.Archivo(nombre)
	if err != nil {
		return nil, fmt.Errorf("servidor: leer %s: %w", nombre, err)
	}
	plantilla, err := template.New(nombre).Parse(string(crudo))
	if err != nil {
		return nil, fmt.Errorf("servidor: interpretar %s: %w", nombre, err)
	}
	var buf bytes.Buffer
	if err := plantilla.Execute(&buf, datos); err != nil {
		return nil, fmt.Errorf("servidor: renderizar %s: %w", nombre, err)
	}
	return buf.Bytes(), nil
}

func (s *Servidor) prepararRutas() error {
	estaticos, err := web.Estaticos()
	if err != nil {
		return fmt.Errorf("servidor: preparar los archivos estáticos: %w", err)
	}

	mux := http.NewServeMux()

	// Páginas. Ninguna de las dos toca la base de datos: abrir /n/{id} no
	// consume la nota, solo muestra la advertencia.
	mux.HandleFunc("GET /{$}", s.mostrarInicio)
	mux.HandleFunc("GET /n/{id}", s.mostrarPaginaDeNota)

	// API.
	mux.HandleFunc("POST /api/notas", s.crearNota)
	mux.HandleFunc("POST /api/notas/{id}/consumir", s.consumirNota)

	// Estáticos y utilidades.
	mux.Handle("GET /estatico/", http.StripPrefix("/estatico/", http.FileServerFS(estaticos)))
	mux.HandleFunc("GET /marca/marca.css", s.hojaDeEstilosDeMarca)
	mux.HandleFunc("GET /marca/{archivo}", s.recursoDeMarca)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /salud", s.salud)

	s.manejador = encadenar(mux,
		s.recuperar,
		cabecerasDeSeguridad,
		s.registro,
	)
	return nil
}

func (s *Servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.manejador.ServeHTTP(w, r)
}

// IniciarTareas arranca el mantenimiento en segundo plano (por ahora, purgar
// del limitador las IP que dejaron de aparecer). Termina al cancelar el
// contexto.
func (s *Servidor) IniciarTareas(ctx context.Context) {
	s.limitador.IniciarPurga(ctx, time.Minute)
}

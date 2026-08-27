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

	paginaInicio []byte
	paginaNota   []byte

	manejador http.Handler
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

// prepararPaginas renderiza una sola vez las páginas embebidas. La de inicio
// lleva inyectado el límite de tamaño para que el contador del navegador y la
// validación del servidor no puedan quedar desincronizados.
func (s *Servidor) prepararPaginas() error {
	crudoInicio, err := web.Plantilla("index.html")
	if err != nil {
		return fmt.Errorf("servidor: leer index.html: %w", err)
	}
	plantilla, err := template.New("index").Parse(string(crudoInicio))
	if err != nil {
		return fmt.Errorf("servidor: interpretar index.html: %w", err)
	}
	var buf bytes.Buffer
	if err := plantilla.Execute(&buf, struct{ MaxBytesNota int }{s.maxBytesNota}); err != nil {
		return fmt.Errorf("servidor: renderizar index.html: %w", err)
	}
	s.paginaInicio = buf.Bytes()

	if s.paginaNota, err = web.Plantilla("nota.html"); err != nil {
		return fmt.Errorf("servidor: leer nota.html: %w", err)
	}
	return nil
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

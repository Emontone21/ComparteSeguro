package servidor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Emontone21/ComparteSeguro/web"
)

// Marca es la identidad visual que muestra la interfaz.
type Marca struct {
	Organizacion string
	Ubicacion    string
	// RutaLogo y RutaFondo son las URL desde donde la página pide cada
	// recurso, o cadena vacía si esa ranura no tiene nada. Las plantillas las
	// usan para decidir si dibujan el logo o el nombre en texto.
	RutaLogo  string
	RutaFondo string
}

// recursoDeMarca es un archivo de marca ya cargado en memoria. Son pocos y
// pequeños; tenerlos cargados evita tocar el disco en cada visita.
type recursoDeMarca struct {
	contenido []byte
	tipo      string
}

// topeArchivoDeMarca descarta por las malas un archivo enorme dejado por error
// en la carpeta de marca, que si no se cargaría entero en memoria.
const topeArchivoDeMarca = 8 << 20 // 8 MB

// ranuraDeMarca es un lugar de la interfaz que admite un archivo, con los
// nombres que se aceptan para llenarlo, por orden de preferencia.
type ranuraDeMarca struct {
	nombre     string
	candidatos []string
}

var ranurasDeMarca = []ranuraDeMarca{
	{"logo", []string{"logo.svg", "logo.png", "logo.webp", "logo.jpg", "logo.jpeg"}},
	{"fondo", []string{"fondo.jpg", "fondo.jpeg", "fondo.png", "fondo.webp", "fondo.svg"}},
}

var tiposDeMarca = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
}

// resolverMarca decide, para cada ranura, qué archivo se va a usar.
//
// Gana el que esté en la carpeta de marca del operador; si ahí no hay nada, se
// cae a lo que viene embebido en el binario. Así el equipo de sistemas puede
// poner el logo y las fotos oficiales montando una carpeta y reiniciando el
// contenedor, sin recompilar nada ni tener Go instalado.
func resolverMarca(carpeta, organizacion, ubicacion string, log *slog.Logger) (Marca, map[string]recursoDeMarca, error) {
	recursos := make(map[string]recursoDeMarca)
	marca := Marca{Organizacion: organizacion, Ubicacion: ubicacion}

	for _, ranura := range ranurasDeMarca {
		nombre, recurso, err := buscarEnRanura(carpeta, ranura, log)
		if err != nil {
			return Marca{}, nil, err
		}
		if nombre == "" {
			continue
		}
		recursos[nombre] = recurso

		ruta := "/marca/" + nombre
		switch ranura.nombre {
		case "logo":
			marca.RutaLogo = ruta
		case "fondo":
			marca.RutaFondo = ruta
		}
	}
	return marca, recursos, nil
}

func buscarEnRanura(carpeta string, ranura ranuraDeMarca, log *slog.Logger) (string, recursoDeMarca, error) {
	for _, candidato := range ranura.candidatos {
		tipo, conocido := tiposDeMarca[strings.ToLower(filepath.Ext(candidato))]
		if !conocido {
			continue
		}

		// Primero la carpeta del operador.
		if carpeta != "" {
			ruta := filepath.Join(carpeta, candidato)
			info, err := os.Stat(ruta)
			switch {
			case err == nil && info.Mode().IsRegular() && info.Size() > topeArchivoDeMarca:
				log.Warn("se ignora un archivo de marca demasiado grande",
					"archivo", candidato, "bytes", info.Size(), "tope", topeArchivoDeMarca)
			case err == nil && info.Mode().IsRegular():
				contenido, err := os.ReadFile(ruta)
				if err != nil {
					return "", recursoDeMarca{}, fmt.Errorf("marca: leer %s: %w", candidato, err)
				}
				log.Info("recurso de marca tomado de la carpeta del operador",
					"ranura", ranura.nombre, "archivo", candidato)
				return candidato, recursoDeMarca{contenido: contenido, tipo: tipo}, nil
			}
		}

		// Si no, lo que venga en el binario.
		if contenido, err := web.Archivo("estatico/marca/" + candidato); err == nil {
			return candidato, recursoDeMarca{contenido: contenido, tipo: tipo}, nil
		}
	}
	return "", recursoDeMarca{}, nil
}

// hojaDeMarca arma la hoja de estilos que expone la foto institucional como
// variable CSS.
//
// Va en un archivo aparte y no en un atributo style= porque la política de
// seguridad de contenido no admite estilos en línea, y no se quiere aflojar esa
// política para acomodar una imagen de fondo.
func hojaDeMarca(marca Marca) []byte {
	fondo := "none"
	if marca.RutaFondo != "" {
		fondo = fmt.Sprintf("url(%q)", marca.RutaFondo)
	}
	return []byte(fmt.Sprintf(":root{--foto-institucional:%s}\n", fondo))
}

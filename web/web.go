// Package web contiene la interfaz, embebida en el binario.
//
// Al ir dentro del ejecutable no hace falta desplegar archivos sueltos ni
// montar volúmenes de solo lectura: el binario es autosuficiente.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html nota.html estatico
var archivos embed.FS

// Plantilla devuelve el contenido de una página del sitio.
func Plantilla(nombre string) ([]byte, error) {
	return archivos.ReadFile(nombre)
}

// Estaticos es el subárbol con CSS y JavaScript.
func Estaticos() (fs.FS, error) {
	return fs.Sub(archivos, "estatico")
}

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

// Archivo devuelve el contenido de un archivo embebido.
func Archivo(nombre string) ([]byte, error) {
	return archivos.ReadFile(nombre)
}

// Estaticos es el subárbol con CSS, JavaScript e imágenes.
func Estaticos() (fs.FS, error) {
	return fs.Sub(archivos, "estatico")
}

// Package config lee la configuración desde variables de entorno.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config reúne todos los parámetros ajustables de la aplicación.
type Config struct {
	// Puerto en el que escucha el servidor. Variable PORT.
	Puerto string
	// Direccion de escucha. Variable BIND_ADDR.
	Direccion string
	// RutaDB es el archivo SQLite. Variable DB_PATH.
	RutaDB string
	// MaxBytesNota es el tamaño máximo del texto en claro, en bytes.
	// Variable MAX_NOTE_BYTES.
	MaxBytesNota int
	// PeticionesPorMinuto y Rafaga configuran el limitador del endpoint de
	// creación. Variables RATE_LIMIT_PER_MINUTE y RATE_LIMIT_BURST.
	PeticionesPorMinuto float64
	Rafaga              int
	// ConfiarEnProxy indica si hay un proxy inverso de confianza delante que
	// añade X-Forwarded-For. Variable TRUST_PROXY.
	ConfiarEnProxy bool
	// FormatoLog es "texto" o "json". Variable LOG_FORMAT.
	FormatoLog string
}

// Valores por omisión, pensados para un despliegue interno típico.
const (
	puertoPorOmision        = "8080"
	direccionPorOmision     = "0.0.0.0"
	rutaDBPorOmision        = "/datos/comparteseguro.db"
	maxBytesNotaPorOmision  = 100 * 1024 // 100 KiB de texto en claro
	porMinutoPorOmision     = 20
	rafagaPorOmision        = 10
	formatoLogPorOmision    = "texto"
	maxBytesNotaTopeAbsurdo = 8 * 1024 * 1024
)

// Cargar arma la configuración a partir del entorno, validando cada valor.
func Cargar() (Config, error) {
	c := Config{
		Puerto:              cadena("PORT", puertoPorOmision),
		Direccion:           cadena("BIND_ADDR", direccionPorOmision),
		RutaDB:              cadena("DB_PATH", rutaDBPorOmision),
		MaxBytesNota:        maxBytesNotaPorOmision,
		PeticionesPorMinuto: porMinutoPorOmision,
		Rafaga:              rafagaPorOmision,
		FormatoLog:          strings.ToLower(cadena("LOG_FORMAT", formatoLogPorOmision)),
	}

	var err error
	if c.MaxBytesNota, err = entero("MAX_NOTE_BYTES", maxBytesNotaPorOmision); err != nil {
		return Config{}, err
	}
	if c.MaxBytesNota < 1 || c.MaxBytesNota > maxBytesNotaTopeAbsurdo {
		return Config{}, fmt.Errorf("config: MAX_NOTE_BYTES debe estar entre 1 y %d, vale %d",
			maxBytesNotaTopeAbsurdo, c.MaxBytesNota)
	}

	porMinuto, err := entero("RATE_LIMIT_PER_MINUTE", porMinutoPorOmision)
	if err != nil {
		return Config{}, err
	}
	if porMinuto < 1 {
		return Config{}, fmt.Errorf("config: RATE_LIMIT_PER_MINUTE debe ser 1 o más, vale %d", porMinuto)
	}
	c.PeticionesPorMinuto = float64(porMinuto)

	if c.Rafaga, err = entero("RATE_LIMIT_BURST", rafagaPorOmision); err != nil {
		return Config{}, err
	}
	if c.Rafaga < 1 {
		return Config{}, fmt.Errorf("config: RATE_LIMIT_BURST debe ser 1 o más, vale %d", c.Rafaga)
	}

	if c.ConfiarEnProxy, err = booleano("TRUST_PROXY", false); err != nil {
		return Config{}, err
	}

	if err := validarPuerto(c.Puerto); err != nil {
		return Config{}, err
	}
	if c.FormatoLog != "texto" && c.FormatoLog != "json" {
		return Config{}, fmt.Errorf(`config: LOG_FORMAT debe ser "texto" o "json", vale %q`, c.FormatoLog)
	}
	return c, nil
}

// DireccionDeEscucha es lo que se le pasa a net/http.
func (c Config) DireccionDeEscucha() string {
	return c.Direccion + ":" + c.Puerto
}

func cadena(nombre, porOmision string) string {
	if v := strings.TrimSpace(os.Getenv(nombre)); v != "" {
		return v
	}
	return porOmision
}

func entero(nombre string, porOmision int) (int, error) {
	v := strings.TrimSpace(os.Getenv(nombre))
	if v == "" {
		return porOmision, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s debe ser un número entero, vale %q", nombre, v)
	}
	return n, nil
}

func booleano(nombre string, porOmision bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(nombre))
	if v == "" {
		return porOmision, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s debe ser true o false, vale %q", nombre, v)
	}
	return b, nil
}

func validarPuerto(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("config: PORT debe ser un número entre 1 y 65535, vale %q", p)
	}
	return nil
}

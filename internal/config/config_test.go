package config

import (
	"strings"
	"testing"
)

func TestValoresPorOmision(t *testing.T) {
	limpiarEntorno(t)

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar devolvió error: %v", err)
	}

	if c.Puerto != puertoPorOmision {
		t.Errorf("Puerto = %q, se esperaba %q", c.Puerto, puertoPorOmision)
	}
	if c.RutaDB != rutaDBPorOmision {
		t.Errorf("RutaDB = %q, se esperaba %q", c.RutaDB, rutaDBPorOmision)
	}
	if c.MaxBytesNota != maxBytesNotaPorOmision {
		t.Errorf("MaxBytesNota = %d, se esperaba %d", c.MaxBytesNota, maxBytesNotaPorOmision)
	}
	if c.ConfiarEnProxy {
		t.Error("ConfiarEnProxy debía ser false por omisión: confiar en X-Forwarded-For sin proxy delante deja saltear el límite de peticiones")
	}
}

func TestElPuertoSeConfiguraPorEntorno(t *testing.T) {
	limpiarEntorno(t)
	t.Setenv("PORT", "9999")

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar devolvió error: %v", err)
	}
	if c.Puerto != "9999" {
		t.Errorf("Puerto = %q, se esperaba \"9999\"", c.Puerto)
	}
	if dir := c.DireccionDeEscucha(); dir != "0.0.0.0:9999" {
		t.Errorf("DireccionDeEscucha() = %q, se esperaba \"0.0.0.0:9999\"", dir)
	}
}

func TestCadaVariableSeLee(t *testing.T) {
	limpiarEntorno(t)
	t.Setenv("PORT", "3000")
	t.Setenv("BIND_ADDR", "127.0.0.1")
	t.Setenv("DB_PATH", "/otro/lado/notas.db")
	t.Setenv("MAX_NOTE_BYTES", "2048")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "5")
	t.Setenv("RATE_LIMIT_BURST", "3")
	t.Setenv("TRUST_PROXY", "true")
	t.Setenv("LOG_FORMAT", "json")

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar devolvió error: %v", err)
	}

	if dir := c.DireccionDeEscucha(); dir != "127.0.0.1:3000" {
		t.Errorf("DireccionDeEscucha() = %q", dir)
	}
	if c.RutaDB != "/otro/lado/notas.db" {
		t.Errorf("RutaDB = %q", c.RutaDB)
	}
	if c.MaxBytesNota != 2048 {
		t.Errorf("MaxBytesNota = %d", c.MaxBytesNota)
	}
	if c.PeticionesPorMinuto != 5 {
		t.Errorf("PeticionesPorMinuto = %v", c.PeticionesPorMinuto)
	}
	if c.Rafaga != 3 {
		t.Errorf("Rafaga = %d", c.Rafaga)
	}
	if !c.ConfiarEnProxy {
		t.Error("ConfiarEnProxy debía ser true")
	}
	if c.FormatoLog != "json" {
		t.Errorf("FormatoLog = %q", c.FormatoLog)
	}
}

func TestRechazaValoresInvalidos(t *testing.T) {
	casos := []struct {
		nombre   string
		variable string
		valor    string
	}{
		{"puerto que no es número", "PORT", "ochomil"},
		{"puerto fuera de rango", "PORT", "70000"},
		{"puerto cero", "PORT", "0"},
		{"tamaño máximo que no es número", "MAX_NOTE_BYTES", "mucho"},
		{"tamaño máximo cero", "MAX_NOTE_BYTES", "0"},
		{"tamaño máximo negativo", "MAX_NOTE_BYTES", "-1"},
		{"tamaño máximo absurdo", "MAX_NOTE_BYTES", "999999999"},
		{"ritmo cero", "RATE_LIMIT_PER_MINUTE", "0"},
		{"ráfaga cero", "RATE_LIMIT_BURST", "0"},
		{"proxy que no es booleano", "TRUST_PROXY", "quizás"},
		{"formato de registro desconocido", "LOG_FORMAT", "xml"},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			limpiarEntorno(t)
			t.Setenv(caso.variable, caso.valor)

			if _, err := Cargar(); err == nil {
				t.Fatalf("se esperaba un error para %s=%q", caso.variable, caso.valor)
			} else if !strings.Contains(err.Error(), caso.variable) {
				t.Errorf("el mensaje de error debería nombrar la variable %s, dice: %v",
					caso.variable, err)
			}
		})
	}
}

// TestUnaVariableEnBlancoUsaElValorPorOmision cubre el caso de un archivo .env
// con una variable declarada pero sin valor, que es un error de dedo bastante
// común. Conviene que caiga en el valor por omisión y no que deje la aplicación
// apuntando a un archivo llamado "  ".
func TestUnaVariableEnBlancoUsaElValorPorOmision(t *testing.T) {
	limpiarEntorno(t)
	t.Setenv("DB_PATH", "   ")
	t.Setenv("PORT", "  ")

	c, err := Cargar()
	if err != nil {
		t.Fatalf("Cargar devolvió error: %v", err)
	}
	if c.RutaDB != rutaDBPorOmision {
		t.Errorf("RutaDB = %q, se esperaba el valor por omisión %q", c.RutaDB, rutaDBPorOmision)
	}
	if c.Puerto != puertoPorOmision {
		t.Errorf("Puerto = %q, se esperaba el valor por omisión %q", c.Puerto, puertoPorOmision)
	}
}

// limpiarEntorno deja las variables de la aplicación sin valor, para que las
// pruebas no dependan del entorno en el que se ejecuten.
func limpiarEntorno(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"PORT", "BIND_ADDR", "DB_PATH", "MAX_NOTE_BYTES",
		"RATE_LIMIT_PER_MINUTE", "RATE_LIMIT_BURST", "TRUST_PROXY", "LOG_FORMAT",
	} {
		t.Setenv(v, "")
	}
}

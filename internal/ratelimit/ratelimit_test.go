package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// relojFalso permite mover el tiempo a mano en las pruebas.
type relojFalso struct {
	mu     sync.Mutex
	actual time.Time
}

func (r *relojFalso) ahora() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.actual
}

func (r *relojFalso) avanzar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actual = r.actual.Add(d)
}

func conRelojFalso(l *Limitador) *relojFalso {
	r := &relojFalso{actual: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l.reloj = r.ahora
	return r
}

func TestPermiteLaRafagaYLuegoCorta(t *testing.T) {
	l := Nuevo(60, 5) // 60 por minuto = 1 por segundo, ráfaga de 5
	conRelojFalso(l)

	for i := range 5 {
		if !l.Permitir("10.0.0.1") {
			t.Fatalf("la petición %d de la ráfaga debía pasar", i+1)
		}
	}
	if l.Permitir("10.0.0.1") {
		t.Fatal("la petición 6 debía rechazarse: la ráfaga es de 5")
	}
}

func TestElCuboSeRellenaConElTiempo(t *testing.T) {
	l := Nuevo(60, 2) // 1 ficha por segundo
	reloj := conRelojFalso(l)

	l.Permitir("10.0.0.1")
	l.Permitir("10.0.0.1")
	if l.Permitir("10.0.0.1") {
		t.Fatal("el cubo debía estar vacío")
	}

	reloj.avanzar(time.Second)
	if !l.Permitir("10.0.0.1") {
		t.Fatal("tras un segundo debía haber una ficha nueva")
	}
	if l.Permitir("10.0.0.1") {
		t.Fatal("solo se había recuperado una ficha")
	}
}

func TestElCuboNoSePasaDeSuCapacidad(t *testing.T) {
	l := Nuevo(60, 3)
	reloj := conRelojFalso(l)

	l.Permitir("10.0.0.1")
	reloj.avanzar(time.Hour) // muchísimo más de lo necesario para llenarlo

	for i := range 3 {
		if !l.Permitir("10.0.0.1") {
			t.Fatalf("la petición %d debía pasar: el cubo estaba lleno", i+1)
		}
	}
	if l.Permitir("10.0.0.1") {
		t.Fatal("el cubo no puede acumular más fichas que su capacidad")
	}
}

func TestCadaClaveTieneSuPropioCubo(t *testing.T) {
	l := Nuevo(60, 2)
	conRelojFalso(l)

	l.Permitir("10.0.0.1")
	l.Permitir("10.0.0.1")
	if l.Permitir("10.0.0.1") {
		t.Fatal("la primera IP debía estar agotada")
	}
	if !l.Permitir("10.0.0.2") {
		t.Fatal("una IP distinta no debe verse afectada por el consumo de otra")
	}
}

func TestEsperaSugerida(t *testing.T) {
	l := Nuevo(60, 1) // 1 ficha por segundo
	conRelojFalso(l)

	if e := l.EsperaSugerida("10.0.0.1"); e != 0 {
		t.Fatalf("sin consumo previo la espera debía ser 0, fue %v", e)
	}
	l.Permitir("10.0.0.1")
	if l.Permitir("10.0.0.1") {
		t.Fatal("el cubo debía estar vacío")
	}
	if e := l.EsperaSugerida("10.0.0.1"); e <= 0 || e > 2*time.Second {
		t.Fatalf("la espera sugerida debía rondar 1 s, fue %v", e)
	}
}

func TestPurgarOlvidaClavesInactivas(t *testing.T) {
	l := Nuevo(60, 2)
	reloj := conRelojFalso(l)

	l.Permitir("10.0.0.1")
	if sacadas := l.Purgar(); sacadas != 0 {
		t.Fatalf("no debía purgar nada todavía, sacó %d", sacadas)
	}

	reloj.avanzar(11 * time.Minute)
	if sacadas := l.Purgar(); sacadas != 1 {
		t.Fatalf("debía purgar 1 clave inactiva, sacó %d", sacadas)
	}
	if n := len(l.cubos); n != 0 {
		t.Fatalf("el mapa debía quedar vacío, tiene %d entradas", n)
	}
}

func TestUsoConcurrenteEntregaExactamenteLasFichasDisponibles(t *testing.T) {
	const rafaga = 50
	const competidores = 500

	l := Nuevo(1, rafaga) // ritmo lentísimo: solo cuentan las fichas iniciales
	conRelojFalso(l)      // el tiempo no avanza, así que no se rellena nada

	var mu sync.Mutex
	permitidas := 0

	var wg sync.WaitGroup
	largada := make(chan struct{})
	for range competidores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-largada
			if l.Permitir("10.0.0.1") {
				mu.Lock()
				permitidas++
				mu.Unlock()
			}
		}()
	}
	close(largada)
	wg.Wait()

	if permitidas != rafaga {
		t.Fatalf("se permitieron %d peticiones, debían ser exactamente %d", permitidas, rafaga)
	}
}

// Package ratelimit implementa un limitador de peticiones por IP con el
// algoritmo de cubo de fichas (token bucket), en memoria y sin dependencias.
//
// Cada IP tiene un cubo con una capacidad máxima de fichas que se rellena a
// ritmo constante. Cada petición consume una ficha; si no quedan, se rechaza.
// Esto tolera ráfagas cortas (alguien que genera tres enlaces seguidos) pero
// acota el ritmo sostenido.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

type cubo struct {
	fichas      float64
	ultimoToque time.Time
}

// Limitador reparte fichas por clave (en esta app, la IP del cliente).
// Es seguro usarlo desde varias goroutines.
type Limitador struct {
	mu     sync.Mutex
	cubos  map[string]*cubo
	tasa   float64 // fichas por segundo
	techo  float64 // capacidad del cubo, o sea el tamaño de ráfaga permitido
	reloj  func() time.Time
	maxIna time.Duration // inactividad tras la cual se olvida una clave
}

// Nuevo construye un limitador que admite ráfagas de hasta `rafaga` peticiones
// y un ritmo sostenido de `porMinuto` peticiones por minuto.
func Nuevo(porMinuto float64, rafaga int) *Limitador {
	if porMinuto <= 0 {
		porMinuto = 1
	}
	if rafaga < 1 {
		rafaga = 1
	}
	return &Limitador{
		cubos:  make(map[string]*cubo),
		tasa:   porMinuto / 60,
		techo:  float64(rafaga),
		reloj:  time.Now,
		maxIna: 10 * time.Minute,
	}
}

// Permitir descuenta una ficha de la clave y dice si la petición sigue adelante.
func (l *Limitador) Permitir(clave string) bool {
	ahora := l.reloj()

	l.mu.Lock()
	defer l.mu.Unlock()

	c, existe := l.cubos[clave]
	if !existe {
		// Un cliente nuevo arranca con el cubo lleno.
		l.cubos[clave] = &cubo{fichas: l.techo - 1, ultimoToque: ahora}
		return true
	}

	// Rellenar según el tiempo transcurrido desde la última petición.
	transcurrido := ahora.Sub(c.ultimoToque).Seconds()
	if transcurrido > 0 {
		c.fichas = min(l.techo, c.fichas+transcurrido*l.tasa)
	}
	c.ultimoToque = ahora

	if c.fichas < 1 {
		return false
	}
	c.fichas--
	return true
}

// EsperaSugerida devuelve cuánto conviene que espere la clave hasta tener otra
// ficha. Alimenta la cabecera Retry-After.
func (l *Limitador) EsperaSugerida(clave string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	c, existe := l.cubos[clave]
	if !existe || c.fichas >= 1 {
		return 0
	}
	segundos := (1 - c.fichas) / l.tasa
	return time.Duration(segundos * float64(time.Second))
}

// Purgar olvida las claves que llevan rato sin aparecer, para que el mapa no
// crezca sin techo. Devuelve cuántas sacó.
func (l *Limitador) Purgar() int {
	limite := l.reloj().Add(-l.maxIna)

	l.mu.Lock()
	defer l.mu.Unlock()

	sacadas := 0
	for clave, c := range l.cubos {
		if c.ultimoToque.Before(limite) {
			delete(l.cubos, clave)
			sacadas++
		}
	}
	return sacadas
}

// IniciarPurga corre Purgar periódicamente hasta que se cancele el contexto.
func (l *Limitador) IniciarPurga(ctx context.Context, cada time.Duration) {
	go func() {
		tic := time.NewTicker(cada)
		defer tic.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tic.C:
				l.Purgar()
			}
		}
	}()
}

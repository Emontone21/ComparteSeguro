package almacen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func almacenDePrueba(t *testing.T) *Almacen {
	t.Helper()
	// Base en archivo, no en memoria: el bloqueo entre conexiones que se quiere
	// verificar solo se comporta de forma realista sobre disco.
	a, err := Abrir(filepath.Join(t.TempDir(), "prueba.db"))
	if err != nil {
		t.Fatalf("no se pudo abrir el almacén: %v", err)
	}
	t.Cleanup(func() { a.Cerrar() })
	return a
}

func TestGuardarYConsumirUnaSolaVez(t *testing.T) {
	a := almacenDePrueba(t)
	ctx := context.Background()
	contenido := []byte("contenido cifrado de mentira")

	if err := a.Guardar(ctx, "abc", contenido, 1); err != nil {
		t.Fatalf("Guardar devolvió error: %v", err)
	}

	leido, err := a.Consumir(ctx, "abc")
	if err != nil {
		t.Fatalf("el primer Consumir devolvió error: %v", err)
	}
	if !bytes.Equal(leido, contenido) {
		t.Fatalf("el contenido leído no coincide con el guardado")
	}

	if _, err := a.Consumir(ctx, "abc"); !errors.Is(err, ErrNoExiste) {
		t.Fatalf("el segundo Consumir debía devolver ErrNoExiste, devolvió: %v", err)
	}
}

func TestConsumirIdentificadorInexistente(t *testing.T) {
	a := almacenDePrueba(t)
	if _, err := a.Consumir(context.Background(), "no-existe"); !errors.Is(err, ErrNoExiste) {
		t.Fatalf("se esperaba ErrNoExiste, se obtuvo: %v", err)
	}
}

func TestGuardarIdentificadorDuplicado(t *testing.T) {
	a := almacenDePrueba(t)
	ctx := context.Background()
	if err := a.Guardar(ctx, "abc", []byte("uno"), 1); err != nil {
		t.Fatalf("Guardar devolvió error: %v", err)
	}
	if err := a.Guardar(ctx, "abc", []byte("dos"), 2); !errors.Is(err, ErrIDDuplicado) {
		t.Fatalf("se esperaba ErrIDDuplicado, se obtuvo: %v", err)
	}
}

// TestConsumirEsAtomicoBajoConcurrencia es la prueba central del modelo de un
// solo uso: muchas goroutines largan a la vez contra la misma nota y solo una
// puede llevarse el contenido.
func TestConsumirEsAtomicoBajoConcurrencia(t *testing.T) {
	const notas = 40
	const competidoresPorNota = 16

	a := almacenDePrueba(t)
	ctx := context.Background()

	for i := range notas {
		id := fmt.Sprintf("nota-%03d", i)
		contenido := []byte(fmt.Sprintf("contenido secreto %03d", i))
		if err := a.Guardar(ctx, id, contenido, int64(i)); err != nil {
			t.Fatalf("Guardar devolvió error: %v", err)
		}
	}

	var erroresInesperados atomic.Int64
	var wg sync.WaitGroup
	largada := make(chan struct{})

	ganadoresPorNota := make([]atomic.Int64, notas)
	for i := range notas {
		id := fmt.Sprintf("nota-%03d", i)
		esperado := []byte(fmt.Sprintf("contenido secreto %03d", i))

		for range competidoresPorNota {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-largada // todas las goroutines arrancan en el mismo instante

				contenido, err := a.Consumir(ctx, id)
				switch {
				case err == nil:
					if !bytes.Equal(contenido, esperado) {
						t.Errorf("nota %s: el contenido entregado no es el esperado", id)
					}
					ganadoresPorNota[i].Add(1)
				case errors.Is(err, ErrNoExiste):
					// Perdió la carrera: es el resultado correcto para todas
					// las peticiones menos una.
				default:
					t.Errorf("nota %s: error inesperado: %v", id, err)
					erroresInesperados.Add(1)
				}
			}()
		}
	}

	close(largada)
	wg.Wait()

	if n := erroresInesperados.Load(); n != 0 {
		t.Fatalf("hubo %d errores inesperados durante la carrera", n)
	}
	for i := range notas {
		if g := ganadoresPorNota[i].Load(); g != 1 {
			t.Errorf("nota %03d: la leyeron %d peticiones, debía leerla exactamente 1", i, g)
		}
	}

	pendientes, err := a.Pendientes(ctx)
	if err != nil {
		t.Fatalf("Pendientes devolvió error: %v", err)
	}
	if pendientes != 0 {
		t.Errorf("quedaron %d notas sin borrar, debían quedar 0", pendientes)
	}
}

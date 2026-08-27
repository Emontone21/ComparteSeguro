// Package almacen guarda y entrega las notas cifradas.
//
// La responsabilidad crítica de este paquete es que una nota se pueda entregar
// exactamente una vez. Consumir() lo garantiza con una única sentencia
// "DELETE ... RETURNING" dentro de una transacción IMMEDIATE: SQLite serializa
// los escritores, así que de dos peticiones simultáneas al mismo identificador
// una obtiene el contenido y la otra encuentra la fila ya borrada. No existe
// una ventana entre "leer" y "borrar" en la que ambas puedan ganar, porque no
// son dos operaciones.
package almacen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // driver SQLite puro Go, sin cgo
)

// ErrNoExiste se devuelve cuando el identificador no está en la base. Puede ser
// porque nunca existió o porque ya fue consumido: quien llama no debe
// distinguir entre ambos casos de cara al usuario.
var ErrNoExiste = errors.New("la nota no existe o ya fue leída")

// ErrIDDuplicado se devuelve si el identificador generado ya estaba en uso.
var ErrIDDuplicado = errors.New("el identificador ya está en uso")

// Almacen es la capa de persistencia sobre SQLite.
type Almacen struct {
	db *sql.DB
}

const esquema = `
CREATE TABLE IF NOT EXISTS notas (
	id        TEXT    PRIMARY KEY,
	contenido BLOB    NOT NULL,
	creada_en INTEGER NOT NULL
) STRICT;
`

// pragmas configura la conexión:
//
//	journal_mode(WAL)   escrituras y lecturas no se bloquean entre sí.
//	busy_timeout(5000)  un escritor que encuentra la base ocupada espera hasta
//	                    5 s en lugar de fallar; es lo que hace que las
//	                    peticiones simultáneas se encolen en vez de romperse.
//	synchronous(NORMAL) compromiso habitual y seguro bajo WAL.
//	temp_store(memory)  ningún temporal en disco (el contenedor corre con el
//	                    sistema de archivos raíz en solo lectura).
//	_txlock=immediate   cada transacción toma el bloqueo de escritura al abrir,
//	                    no al primer escribir: evita fallos por promoción de
//	                    bloqueo bajo concurrencia.
const pragmas = "_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=temp_store(memory)" +
	"&_pragma=foreign_keys(on)" +
	"&_txlock=immediate"

// Abrir abre (o crea) la base en la ruta indicada y aplica el esquema.
func Abrir(ruta string) (*Almacen, error) {
	if strings.TrimSpace(ruta) == "" {
		return nil, errors.New("almacen: la ruta de la base de datos está vacía")
	}

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", ruta, pragmas))
	if err != nil {
		return nil, fmt.Errorf("almacen: abrir la base: %w", err)
	}

	// SQLite admite un solo escritor a la vez; un puñado de conexiones alcanza
	// y sobra para una app interna, y busy_timeout absorbe la contención.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("almacen: conectar con la base: %w", err)
	}
	if _, err := db.Exec(esquema); err != nil {
		db.Close()
		return nil, fmt.Errorf("almacen: aplicar el esquema: %w", err)
	}
	return &Almacen{db: db}, nil
}

// Cerrar libera la base de datos.
func (a *Almacen) Cerrar() error {
	return a.db.Close()
}

// Guardar persiste una nota ya cifrada. El almacén nunca ve texto en claro ni
// la clave: contenido es el resultado del cifrado hecho en el navegador.
func (a *Almacen) Guardar(ctx context.Context, id string, contenido []byte, creadaEn int64) error {
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO notas (id, contenido, creada_en) VALUES (?, ?, ?)`,
		id, contenido, creadaEn)
	if err != nil {
		if esViolacionDeUnicidad(err) {
			return ErrIDDuplicado
		}
		return fmt.Errorf("almacen: guardar la nota: %w", err)
	}
	return nil
}

// Consumir entrega el contenido de una nota y la borra, de forma atómica.
//
// Devuelve ErrNoExiste si el identificador no está: sea porque nunca existió,
// sea porque otra petición ganó la carrera y ya se la llevó.
func (a *Almacen) Consumir(ctx context.Context, id string) ([]byte, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("almacen: abrir la transacción: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op si el Commit ya corrió

	// Una sola sentencia: el borrado y la entrega del contenido son la misma
	// operación. Si esta transacción llega a ver la fila, es porque nadie más
	// la vio ni la va a ver.
	filas, err := tx.QueryContext(ctx,
		`DELETE FROM notas WHERE id = ? RETURNING contenido`, id)
	if err != nil {
		return nil, fmt.Errorf("almacen: consumir la nota: %w", err)
	}

	var contenido []byte
	encontrada := false
	for filas.Next() {
		var b []byte
		if err := filas.Scan(&b); err != nil {
			filas.Close()
			return nil, fmt.Errorf("almacen: leer el contenido: %w", err)
		}
		contenido, encontrada = b, true
	}
	// Agotar el cursor antes del Commit no es opcional: es lo que garantiza que
	// la sentencia DELETE corrió hasta el final y no quedó a medio ejecutar.
	if err := filas.Err(); err != nil {
		filas.Close()
		return nil, fmt.Errorf("almacen: recorrer el resultado: %w", err)
	}
	if err := filas.Close(); err != nil {
		return nil, fmt.Errorf("almacen: cerrar el resultado: %w", err)
	}

	if !encontrada {
		return nil, ErrNoExiste
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("almacen: confirmar la transacción: %w", err)
	}
	return contenido, nil
}

// Pendientes informa cuántas notas quedan sin leer. Se usa en las pruebas y en
// el endpoint de salud; nunca expone identificadores ni contenido.
func (a *Almacen) Pendientes(ctx context.Context) (int, error) {
	var n int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notas`).Scan(&n); err != nil {
		return 0, fmt.Errorf("almacen: contar las notas: %w", err)
	}
	return n, nil
}

func esViolacionDeUnicidad(err error) bool {
	// El driver puro Go no expone un tipo de error estable para esto, así que
	// se inspecciona el mensaje, que sí lo es.
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

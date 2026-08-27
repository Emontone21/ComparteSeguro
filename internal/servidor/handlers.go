package servidor

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/Emontone21/ComparteSeguro/internal/almacen"
)

// --- Cuerpos de las peticiones y respuestas ------------------------------

type peticionCrear struct {
	// Contenido es el bloque ya cifrado, en base64url. El servidor no tiene la
	// clave y no puede leerlo.
	Contenido string `json:"contenido"`
}

type respuestaCrear struct {
	// Solo el identificador. La URL final la arma el navegador, que es el único
	// que conoce la clave.
	ID string `json:"id"`
}

type respuestaConsumir struct {
	Contenido string `json:"contenido"`
}

type respuestaError struct {
	Error string `json:"error"`
}

// --- Páginas -------------------------------------------------------------

func (s *Servidor) mostrarInicio(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.paginaInicio)
}

// mostrarPaginaDeNota entrega la pantalla intermedia. A propósito no consulta
// la base: si respondiera distinto según la nota existiera o no, bastaría con
// visitar una URL para saber si hay una nota esperando, y además cualquier
// escáner de enlaces revelaría su existencia.
func (s *Servidor) mostrarPaginaDeNota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.paginaNota)
}

// hojaDeEstilosDeMarca entrega la hoja generada al arrancar, que expone la
// foto institucional como variable CSS.
func (s *Servidor) hojaDeEstilosDeMarca(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(s.hojaMarca)
}

// recursoDeMarca entrega el logo o la foto institucional.
//
// Solo responde por los nombres que se resolvieron al arrancar: la búsqueda es
// una consulta exacta a un mapa, así que no hay forma de pedir un archivo de
// fuera de la carpeta de marca por más que se disfrace la ruta.
func (s *Servidor) recursoDeMarca(w http.ResponseWriter, r *http.Request) {
	recurso, existe := s.recursosMarca[r.PathValue("archivo")]
	if !existe {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", recurso.tipo)
	w.Write(recurso.contenido)
}

func (s *Servidor) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func (s *Servidor) salud(w http.ResponseWriter, r *http.Request) {
	if _, err := s.almacen.Pendientes(r.Context()); err != nil {
		s.log.Error("la comprobación de salud falló", "error", err)
		responderError(w, http.StatusServiceUnavailable, "La base de datos no responde.")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}

// --- API -----------------------------------------------------------------

// crearNota guarda una nota ya cifrada y devuelve su identificador.
func (s *Servidor) crearNota(w http.ResponseWriter, r *http.Request) {
	ip := s.ipDelCliente(r)
	if !s.limitador.Permitir(ip) {
		espera := s.limitador.EsperaSugerida(ip)
		segundos := int(math.Ceil(espera.Seconds()))
		if segundos < 1 {
			segundos = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(segundos))
		responderError(w, http.StatusTooManyRequests,
			"Estás generando enlaces demasiado rápido. Esperá unos segundos e intentá de nuevo.")
		return
	}

	// Cortar el cuerpo antes de leerlo: si alguien manda un cuerpo gigante, se
	// corta acá y no llega a ocupar memoria.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBytesCuerpo)

	var pet peticionCrear
	decodificador := json.NewDecoder(r.Body)
	decodificador.DisallowUnknownFields()
	if err := decodificador.Decode(&pet); err != nil {
		var errDemasiadoGrande *http.MaxBytesError
		if errors.As(err, &errDemasiadoGrande) {
			responderError(w, http.StatusRequestEntityTooLarge, s.mensajeDemasiadoGrande())
			return
		}
		responderError(w, http.StatusBadRequest, "La petición no tiene el formato esperado.")
		return
	}

	contenido, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(pet.Contenido, "="))
	if err != nil {
		responderError(w, http.StatusBadRequest, "El contenido cifrado no tiene el formato esperado.")
		return
	}

	// Un bloque AES-GCM no puede ser más corto que el vector de inicialización
	// más la etiqueta de autenticación, ni siquiera cifrando la cadena vacía.
	if len(contenido) < sobrecargaCifrado {
		responderError(w, http.StatusBadRequest, "El contenido cifrado no tiene el formato esperado.")
		return
	}
	if len(contenido) > s.maxBytesNota+sobrecargaCifrado {
		responderError(w, http.StatusRequestEntityTooLarge, s.mensajeDemasiadoGrande())
		return
	}

	// Se reintenta por si el identificador ya existiera. Con 144 bits esto no
	// va a pasar nunca, pero un choque silencioso pisaría una nota ajena, así
	// que conviene tratarlo igual.
	var id string
	for intento := range 3 {
		if id, err = nuevoID(); err != nil {
			s.log.Error("no se pudo generar el identificador", "error", err)
			responderError(w, http.StatusInternalServerError, "No se pudo generar el enlace.")
			return
		}
		err = s.almacen.Guardar(r.Context(), id, contenido, s.reloj().Unix())
		if err == nil {
			break
		}
		if !errors.Is(err, almacen.ErrIDDuplicado) {
			// El error se registra tal cual; los errores del almacén no
			// incluyen el identificador ni el contenido.
			s.log.Error("no se pudo guardar la nota", "error", err, "intento", intento+1)
			responderError(w, http.StatusInternalServerError, "No se pudo guardar la nota.")
			return
		}
	}
	if err != nil {
		s.log.Error("no se pudo encontrar un identificador libre")
		responderError(w, http.StatusInternalServerError, "No se pudo generar el enlace.")
		return
	}

	responderJSON(w, http.StatusCreated, respuestaCrear{ID: id})
}

// consumirNota entrega el contenido de una nota y la destruye, en una sola
// operación atómica. Es POST y no GET a propósito: los escáneres de enlaces de
// los clientes de correo y de mensajería visitan las URL que reciben, y con un
// GET quemarían la nota antes de que la abra el destinatario.
func (s *Servidor) consumirNota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Un identificador mal formado se responde igual que uno inexistente, para
	// no dar ninguna señal distinta.
	if !identificadorValido(id) {
		responderError(w, http.StatusNotFound, mensajeNotaAusente)
		return
	}

	contenido, err := s.almacen.Consumir(r.Context(), id)
	if err != nil {
		if errors.Is(err, almacen.ErrNoExiste) {
			responderError(w, http.StatusNotFound, mensajeNotaAusente)
			return
		}
		s.log.Error("no se pudo consumir la nota", "error", err)
		responderError(w, http.StatusInternalServerError, "No se pudo leer la nota.")
		return
	}

	responderJSON(w, http.StatusOK, respuestaConsumir{
		Contenido: base64.RawURLEncoding.EncodeToString(contenido),
	})
}

// --- Utilidades ----------------------------------------------------------

// nuevoID genera un identificador aleatorio con 144 bits de entropía usando el
// generador criptográfico del sistema. crypto/rand, no math/rand: el
// identificador es la mitad del secreto de una nota, y uno predecible dejaría
// adivinar enlaces ajenos.
func nuevoID() (string, error) {
	crudo := make([]byte, largoIDBytes)
	if _, err := rand.Read(crudo); err != nil {
		return "", fmt.Errorf("generar el identificador: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(crudo), nil
}

// identificadorValido comprueba la forma del identificador sin consultar la
// base, para descartar basura antes de tocar el disco.
func identificadorValido(id string) bool {
	if len(id) != largoIDTexto {
		return false
	}
	for i := range len(id) {
		c := id[i]
		esValido := (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_'
		if !esValido {
			return false
		}
	}
	return true
}

func (s *Servidor) mensajeDemasiadoGrande() string {
	return fmt.Sprintf("La nota supera el límite de %d KB.", s.maxBytesNota/1024)
}

func responderJSON(w http.ResponseWriter, estado int, cuerpo any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(estado)
	// Si la codificación fallara a esta altura ya se envió la cabecera y no hay
	// nada mejor que hacer que cortar la respuesta.
	json.NewEncoder(w).Encode(cuerpo) //nolint:errcheck
}

func responderError(w http.ResponseWriter, estado int, mensaje string) {
	responderJSON(w, estado, respuestaError{Error: mensaje})
}

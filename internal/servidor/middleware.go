package servidor

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// politicaCSP es deliberadamente restrictiva: la página no carga nada de
// terceros, no se deja incrustar, no envía formularios a ningún lado y no
// admite scripts en línea. Con esto, aunque alguien lograra inyectar HTML, no
// tendría por dónde ejecutar código ni por dónde sacar el contenido.
const politicaCSP = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self'; " +
	"connect-src 'self'; " +
	"font-src 'self'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'; " +
	"object-src 'none'"

// cabecerasDeSeguridad aplica a toda respuesta las cabeceras que evitan que la
// nota quede cacheada, indexada, o incrustada en una página ajena.
func cabecerasDeSeguridad(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// Que nada de esto se guarde en disco ni en un proxy intermedio.
		h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		h.Set("Pragma", "no-cache")
		h.Set("Expires", "0")

		// Que no se pueda incrustar en un iframe (defensa contra clickjacking:
		// si no se puede incrustar, no se puede engañar a nadie para que
		// presione "Ver nota" sin darse cuenta).
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", politicaCSP)

		// Que la URL, que contiene el identificador, no se filtre al navegar
		// hacia afuera.
		h.Set("Referrer-Policy", "no-referrer")

		// Que ningún buscador ni archivador guarde nada.
		h.Set("X-Robots-Tag", "noindex, nofollow, noarchive, nosnippet")

		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")

		siguiente.ServeHTTP(w, r)
	})
}

// escritorConEstado recuerda el código de estado para poder registrarlo.
type escritorConEstado struct {
	http.ResponseWriter
	estado int
	bytes  int
}

func (e *escritorConEstado) WriteHeader(codigo int) {
	if e.estado == 0 {
		e.estado = codigo
	}
	e.ResponseWriter.WriteHeader(codigo)
}

func (e *escritorConEstado) Write(b []byte) (int, error) {
	if e.estado == 0 {
		e.estado = http.StatusOK
	}
	n, err := e.ResponseWriter.Write(b)
	e.bytes += n
	return n, err
}

// etiquetaRuta reduce una ruta concreta a la forma genérica de su endpoint.
//
// Es la pieza que impide que un identificador termine en los registros: se
// registra "/n/{id}", nunca "/n/AbCd...". El identificador es, junto con la
// clave, la mitad del secreto; un registro que lo guardara convertiría el
// archivo de logs en una lista de notas pendientes de leer.
func etiquetaRuta(ruta string) string {
	switch {
	case ruta == "/":
		return "/"
	case ruta == "/robots.txt", ruta == "/salud", ruta == "/favicon.ico":
		return ruta
	case ruta == "/api/notas":
		return "/api/notas"
	case strings.HasPrefix(ruta, "/api/notas/"):
		return "/api/notas/{id}/consumir"
	case strings.HasPrefix(ruta, "/n/"):
		return "/n/{id}"
	case strings.HasPrefix(ruta, "/estatico/"):
		return "/estatico/*"
	case strings.HasPrefix(ruta, "/marca/"):
		return "/marca/*"
	default:
		return "(otra)"
	}
}

// registro deja constancia de cada petición sin anotar jamás el identificador
// de una nota, su contenido, ni la URL completa.
func (s *Servidor) registro(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		envuelto := &escritorConEstado{ResponseWriter: w}

		siguiente.ServeHTTP(envuelto, r)

		estado := envuelto.estado
		if estado == 0 {
			estado = http.StatusOK
		}

		s.log.Info("petición",
			"metodo", r.Method,
			"ruta", etiquetaRuta(r.URL.Path), // forma genérica, nunca la real
			"estado", estado,
			"bytes", envuelto.bytes,
			"ms", time.Since(inicio).Milliseconds(),
			"ip", s.ipDelCliente(r),
		)
	})
}

// recuperar evita que un pánico tire el proceso y que su detalle se le escape
// al cliente.
func (s *Servidor) recuperar(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				// Se registra la ruta genérica, no la real, por lo mismo de
				// siempre: la real lleva el identificador.
				s.log.Error("pánico atendiendo una petición",
					"ruta", etiquetaRuta(r.URL.Path),
					"pánico", p,
				)
				responderError(w, http.StatusInternalServerError, "Error interno del servidor.")
			}
		}()
		siguiente.ServeHTTP(w, r)
	})
}

// ipDelCliente determina la IP a efectos del limitador y del registro.
//
// Solo se mira X-Forwarded-For si el operador declaró que hay un proxy inverso
// de confianza delante (TRUST_PROXY): si no, cualquiera podría inventarse la
// cabecera y saltearse el límite de peticiones. Cuando se la mira, se toma la
// última entrada, que es la que agrega el propio proxy; las anteriores las
// puede haber escrito el cliente.
func (s *Servidor) ipDelCliente(r *http.Request) string {
	if s.confiarEnProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			partes := strings.Split(xff, ",")
			ultima := strings.TrimSpace(partes[len(partes)-1])
			if ultima != "" {
				return ultima
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// encadenar aplica los middlewares en el orden en que se los pasa.
func encadenar(h http.Handler, capas ...func(http.Handler) http.Handler) http.Handler {
	for i := len(capas) - 1; i >= 0; i-- {
		h = capas[i](h)
	}
	return h
}

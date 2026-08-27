package servidor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Emontone21/ComparteSeguro/internal/almacen"
)

// --- Andamiaje -----------------------------------------------------------

// bufferSeguro deja que el registro se escriba desde varias goroutines sin que
// el detector de carreras proteste.
type bufferSeguro struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bufferSeguro) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bufferSeguro) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type entorno struct {
	app  *Servidor
	http *httptest.Server
	logs *bufferSeguro
	alm  *almacen.Almacen
	base string
}

type opcionesPrueba struct {
	maxBytesNota        int
	peticionesPorMinuto float64
	rafaga              int
	confiarEnProxy      bool
	organizacion        string
	ubicacion           string
	directorioMarca     string
}

func montar(t *testing.T, ajustes ...func(*opcionesPrueba)) *entorno {
	t.Helper()

	o := opcionesPrueba{
		maxBytesNota: 100 * 1024,
		// Por omisión el limitador no debe estorbar; las pruebas que lo
		// ejercitan lo bajan a mano.
		peticionesPorMinuto: 1_000_000,
		rafaga:              1_000_000,
		organizacion:        "UTE",
		ubicacion:           "Montevideo, Uruguay",
	}
	for _, ajustar := range ajustes {
		ajustar(&o)
	}

	alm, err := almacen.Abrir(filepath.Join(t.TempDir(), "prueba.db"))
	if err != nil {
		t.Fatalf("no se pudo abrir el almacén: %v", err)
	}
	t.Cleanup(func() { alm.Cerrar() })

	logs := &bufferSeguro{}
	app, err := Nuevo(Opciones{
		Almacen:             alm,
		Log:                 slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		MaxBytesNota:        o.maxBytesNota,
		PeticionesPorMinuto: o.peticionesPorMinuto,
		Rafaga:              o.rafaga,
		ConfiarEnProxy:      o.confiarEnProxy,
		Organizacion:        o.organizacion,
		Ubicacion:           o.ubicacion,
		DirectorioMarca:     o.directorioMarca,
	})
	if err != nil {
		t.Fatalf("no se pudo construir el servidor: %v", err)
	}

	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	return &entorno{app: app, http: srv, logs: logs, alm: alm, base: srv.URL}
}

// crear sube una nota ya "cifrada" (para el servidor es una bolsa de bytes
// opaca) y devuelve el identificador.
func (e *entorno) crear(t *testing.T, contenido []byte) string {
	t.Helper()
	resp, cuerpo := e.crearCrudo(t, contenido)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("crear devolvió %d, se esperaba 201. Cuerpo: %s", resp.StatusCode, cuerpo)
	}
	var r respuestaCrear
	if err := json.Unmarshal(cuerpo, &r); err != nil {
		t.Fatalf("no se pudo interpretar la respuesta de creación: %v", err)
	}
	if r.ID == "" {
		t.Fatal("la respuesta de creación no trajo identificador")
	}
	return r.ID
}

func (e *entorno) crearCrudo(t *testing.T, contenido []byte) (*http.Response, []byte) {
	t.Helper()
	cuerpo, err := json.Marshal(peticionCrear{
		Contenido: base64.RawURLEncoding.EncodeToString(contenido),
	})
	if err != nil {
		t.Fatalf("no se pudo armar la petición: %v", err)
	}
	resp, err := http.Post(e.base+"/api/notas", "application/json", bytes.NewReader(cuerpo))
	if err != nil {
		t.Fatalf("la petición de creación falló: %v", err)
	}
	defer resp.Body.Close()
	leido, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("no se pudo leer la respuesta: %v", err)
	}
	return resp, leido
}

func (e *entorno) consumir(t *testing.T, id string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(e.base+"/api/notas/"+id+"/consumir", "application/json", nil)
	if err != nil {
		t.Fatalf("la petición de consumo falló: %v", err)
	}
	defer resp.Body.Close()
	cuerpo, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("no se pudo leer la respuesta: %v", err)
	}
	return resp, cuerpo
}

// contenidoDe extrae y decodifica el contenido de una respuesta de consumo.
func contenidoDe(t *testing.T, cuerpo []byte) []byte {
	t.Helper()
	var r respuestaConsumir
	if err := json.Unmarshal(cuerpo, &r); err != nil {
		t.Fatalf("no se pudo interpretar la respuesta de consumo: %v", err)
	}
	bytesContenido, err := base64.RawURLEncoding.DecodeString(r.Contenido)
	if err != nil {
		t.Fatalf("el contenido devuelto no era base64url válido: %v", err)
	}
	return bytesContenido
}

// --- Camino crítico ------------------------------------------------------

// TestCaminoCritico recorre el flujo completo: crear una nota, comprobar que
// abrir la página no la consume, leerla una vez, y comprobar que el segundo
// acceso falla.
func TestCaminoCritico(t *testing.T) {
	e := montar(t)
	original := []byte("bloque cifrado que el servidor no puede leer")

	id := e.crear(t, original)

	// Paso 1: abrir la pantalla intermedia NO debe destruir nada.
	resp, err := http.Get(e.base + "/n/" + id)
	if err != nil {
		t.Fatalf("no se pudo abrir la página de la nota: %v", err)
	}
	pagina, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("la página de la nota devolvió %d, se esperaba 200", resp.StatusCode)
	}
	if !bytes.Contains(pagina, []byte("Esta nota se destruirá al abrirla")) {
		t.Error("la pantalla intermedia no muestra la advertencia esperada")
	}
	if pendientes, _ := e.alm.Pendientes(context.Background()); pendientes != 1 {
		t.Fatalf("abrir la página consumió la nota: quedan %d, debía quedar 1", pendientes)
	}

	// Paso 2: la primera lectura entrega el contenido.
	resp, cuerpo := e.consumir(t, id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("el primer consumo devolvió %d, se esperaba 200. Cuerpo: %s", resp.StatusCode, cuerpo)
	}
	if leido := contenidoDe(t, cuerpo); !bytes.Equal(leido, original) {
		t.Errorf("el contenido entregado no coincide con el guardado")
	}

	// Paso 3: el segundo acceso ya no encuentra nada.
	resp, cuerpo = e.consumir(t, id)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("el segundo consumo devolvió %d, se esperaba 404", resp.StatusCode)
	}
	if !strings.Contains(string(cuerpo), mensajeNotaAusente) {
		t.Errorf("el segundo consumo no devolvió el mensaje esperado, devolvió: %s", cuerpo)
	}

	if pendientes, _ := e.alm.Pendientes(context.Background()); pendientes != 0 {
		t.Errorf("la nota no se borró: quedan %d", pendientes)
	}
}

// TestNoSeDistingueLeidaDeInexistente comprueba que el servidor responde
// exactamente lo mismo ante una nota ya leída y ante una que nunca existió. Si
// se distinguieran, probar identificadores al azar revelaría cuáles llegaron a
// existir.
func TestNoSeDistingueLeidaDeInexistente(t *testing.T) {
	e := montar(t)

	id := e.crear(t, bytes.Repeat([]byte("x"), 64))
	if resp, _ := e.consumir(t, id); resp.StatusCode != http.StatusOK {
		t.Fatalf("el primer consumo debía funcionar, devolvió %d", resp.StatusCode)
	}

	respLeida, cuerpoLeida := e.consumir(t, id)

	// Un identificador con la forma correcta que nunca existió.
	inexistente, err := nuevoID()
	if err != nil {
		t.Fatalf("no se pudo generar un identificador: %v", err)
	}
	respInexistente, cuerpoInexistente := e.consumir(t, inexistente)

	if respLeida.StatusCode != respInexistente.StatusCode {
		t.Errorf("los códigos difieren: leída=%d, inexistente=%d",
			respLeida.StatusCode, respInexistente.StatusCode)
	}
	if !bytes.Equal(cuerpoLeida, cuerpoInexistente) {
		t.Errorf("los cuerpos difieren:\n  leída:       %s\n  inexistente: %s",
			cuerpoLeida, cuerpoInexistente)
	}

	// Y un identificador directamente mal formado tampoco debe delatarse.
	respBasura, cuerpoBasura := e.consumir(t, "esto-no-es-un-identificador")
	if respBasura.StatusCode != respInexistente.StatusCode {
		t.Errorf("un identificador mal formado responde %d y uno inexistente %d",
			respBasura.StatusCode, respInexistente.StatusCode)
	}
	if !bytes.Equal(cuerpoBasura, cuerpoInexistente) {
		t.Errorf("un identificador mal formado responde distinto:\n  basura:      %s\n  inexistente: %s",
			cuerpoBasura, cuerpoInexistente)
	}
}

// TestConcurrenciaSoloUnaPeticionLeeLaNota es la prueba de la carrera, ahora
// sobre HTTP real: varias peticiones simultáneas al mismo enlace y una sola
// puede llevarse el contenido.
func TestConcurrenciaSoloUnaPeticionLeeLaNota(t *testing.T) {
	const notas = 25
	const competidoresPorNota = 12

	e := montar(t)

	ids := make([]string, notas)
	contenidos := make([][]byte, notas)
	for i := range notas {
		// El contenido tiene que superar el mínimo que exige el servidor: un
		// bloque AES-GCM nunca puede ser más corto que su vector de
		// inicialización más su etiqueta de autenticación.
		contenidos[i] = []byte(fmt.Sprintf("bloque cifrado de prueba, el numero %02d de la tanda", i))
		ids[i] = e.crear(t, contenidos[i])
	}

	ganadores := make([]atomic.Int64, notas)
	var respuestasRaras atomic.Int64

	var wg sync.WaitGroup
	largada := make(chan struct{})

	for i := range notas {
		for range competidoresPorNota {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-largada

				resp, err := http.Post(e.base+"/api/notas/"+ids[i]+"/consumir", "application/json", nil)
				if err != nil {
					t.Errorf("la petición falló: %v", err)
					respuestasRaras.Add(1)
					return
				}
				cuerpo, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				switch resp.StatusCode {
				case http.StatusOK:
					var r respuestaConsumir
					if err := json.Unmarshal(cuerpo, &r); err != nil {
						t.Errorf("respuesta ilegible: %v", err)
						respuestasRaras.Add(1)
						return
					}
					entregado, err := base64.RawURLEncoding.DecodeString(r.Contenido)
					if err != nil || !bytes.Equal(entregado, contenidos[i]) {
						t.Errorf("nota %d: el contenido entregado no es el esperado", i)
						respuestasRaras.Add(1)
						return
					}
					ganadores[i].Add(1)
				case http.StatusNotFound:
					// Perdió la carrera: es lo correcto.
				default:
					t.Errorf("nota %d: estado inesperado %d (%s)", i, resp.StatusCode, cuerpo)
					respuestasRaras.Add(1)
				}
			}()
		}
	}

	close(largada)
	wg.Wait()

	if n := respuestasRaras.Load(); n != 0 {
		t.Fatalf("hubo %d respuestas inesperadas durante la carrera", n)
	}
	for i := range notas {
		if g := ganadores[i].Load(); g != 1 {
			t.Errorf("nota %d: la leyeron %d peticiones de %d, debía leerla exactamente 1",
				i, g, competidoresPorNota)
		}
	}

	if pendientes, _ := e.alm.Pendientes(context.Background()); pendientes != 0 {
		t.Errorf("quedaron %d notas sin borrar", pendientes)
	}
}

// --- Validación de entrada -----------------------------------------------

func TestRechazaNotaDemasiadoGrande(t *testing.T) {
	const limite = 1024
	e := montar(t, func(o *opcionesPrueba) { o.maxBytesNota = limite })

	casos := []struct {
		nombre string
		bytes  int
	}{
		// Apenas por encima del límite: lo tiene que atajar la validación del
		// contenido decodificado.
		{"apenas por encima del límite", limite + sobrecargaCifrado + 100},
		// Muy por encima: lo ataja el corte del cuerpo, antes de leerlo entero.
		{"muy por encima del límite", limite * 20},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			resp, cuerpo := e.crearCrudo(t, bytes.Repeat([]byte("x"), caso.bytes))
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("se esperaba 413, se obtuvo %d. Cuerpo: %s", resp.StatusCode, cuerpo)
			}
		})
	}

	// Justo en el límite sí tiene que entrar.
	resp, cuerpo := e.crearCrudo(t, bytes.Repeat([]byte("x"), limite+sobrecargaCifrado))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("una nota justo en el límite debía aceptarse, se obtuvo %d. Cuerpo: %s",
			resp.StatusCode, cuerpo)
	}
}

func TestRechazaContenidoCifradoImposible(t *testing.T) {
	e := montar(t)

	// Más corto que el vector de inicialización más la etiqueta de
	// autenticación: no puede ser la salida de AES-GCM.
	resp, cuerpo := e.crearCrudo(t, bytes.Repeat([]byte("x"), sobrecargaCifrado-1))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("se esperaba 400, se obtuvo %d. Cuerpo: %s", resp.StatusCode, cuerpo)
	}
}

func TestRechazaCuerposMalFormados(t *testing.T) {
	e := montar(t)

	casos := []struct {
		nombre string
		cuerpo string
	}{
		{"no es JSON", "esto no es json"},
		{"contenido que no es base64url", `{"contenido":"no es base64 válido !!!"}`},
		{"campos desconocidos", `{"contenido":"AAAA","clave":"deberia-rechazarse"}`},
		{"cuerpo vacío", ``},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			resp, err := http.Post(e.base+"/api/notas", "application/json", strings.NewReader(caso.cuerpo))
			if err != nil {
				t.Fatalf("la petición falló: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("se esperaba 400, se obtuvo %d", resp.StatusCode)
			}
		})
	}
}

func TestMetodosNoPermitidos(t *testing.T) {
	e := montar(t)
	id := e.crear(t, bytes.Repeat([]byte("x"), 64))

	// Un GET no puede consumir una nota: es lo que impide que un escáner de
	// enlaces la queme con solo visitarla.
	resp, err := http.Get(e.base + "/api/notas/" + id + "/consumir")
	if err != nil {
		t.Fatalf("la petición falló: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET sobre el endpoint de consumo devolvió %d, se esperaba 405", resp.StatusCode)
	}
	if pendientes, _ := e.alm.Pendientes(context.Background()); pendientes != 1 {
		t.Error("un GET sobre el endpoint de consumo destruyó la nota")
	}

	resp, err = http.Get(e.base + "/api/notas")
	if err != nil {
		t.Fatalf("la petición falló: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET sobre el endpoint de creación devolvió %d, se esperaba 405", resp.StatusCode)
	}
}

// --- Límite de peticiones ------------------------------------------------

func TestLimiteDePeticionesPorIP(t *testing.T) {
	const rafaga = 4
	e := montar(t, func(o *opcionesPrueba) {
		o.peticionesPorMinuto = 1 // ritmo lentísimo: solo cuenta la ráfaga
		o.rafaga = rafaga
	})

	contenido := bytes.Repeat([]byte("x"), 64)
	for i := range rafaga {
		if resp, cuerpo := e.crearCrudo(t, contenido); resp.StatusCode != http.StatusCreated {
			t.Fatalf("la petición %d de la ráfaga devolvió %d. Cuerpo: %s", i+1, resp.StatusCode, cuerpo)
		}
	}

	resp, _ := e.crearCrudo(t, contenido)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("pasada la ráfaga se esperaba 429, se obtuvo %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("la respuesta 429 debía traer la cabecera Retry-After")
	}

	// El límite es solo para crear: leer una nota existente no debe verse
	// afectado.
	if resp, _ := e.consumir(t, "identificador-inexistente"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("el consumo devolvió %d, el limitador no debería alcanzarlo", resp.StatusCode)
	}
}

func TestNoSeConfiaEnXForwardedForSalvoQueSeIndique(t *testing.T) {
	enviarConXFF := func(t *testing.T, e *entorno, ip string) int {
		t.Helper()
		cuerpo, _ := json.Marshal(peticionCrear{
			Contenido: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("x"), 64)),
		})
		pet, err := http.NewRequest(http.MethodPost, e.base+"/api/notas", bytes.NewReader(cuerpo))
		if err != nil {
			t.Fatalf("no se pudo armar la petición: %v", err)
		}
		pet.Header.Set("Content-Type", "application/json")
		pet.Header.Set("X-Forwarded-For", ip)
		resp, err := http.DefaultClient.Do(pet)
		if err != nil {
			t.Fatalf("la petición falló: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("sin confiar en el proxy la cabecera se ignora", func(t *testing.T) {
		e := montar(t, func(o *opcionesPrueba) {
			o.peticionesPorMinuto, o.rafaga, o.confiarEnProxy = 1, 2, false
		})
		enviarConXFF(t, e, "1.1.1.1")
		enviarConXFF(t, e, "2.2.2.2")
		// Si la cabecera se tuviera en cuenta, cada IP inventada tendría su
		// propio cubo y esta tercera petición pasaría.
		if estado := enviarConXFF(t, e, "3.3.3.3"); estado != http.StatusTooManyRequests {
			t.Fatalf("cambiar X-Forwarded-For permitió saltear el límite: estado %d", estado)
		}
	})

	t.Run("confiando en el proxy la cabecera manda", func(t *testing.T) {
		e := montar(t, func(o *opcionesPrueba) {
			o.peticionesPorMinuto, o.rafaga, o.confiarEnProxy = 1, 1, true
		})
		if estado := enviarConXFF(t, e, "1.1.1.1"); estado != http.StatusCreated {
			t.Fatalf("la primera petición devolvió %d", estado)
		}
		if estado := enviarConXFF(t, e, "1.1.1.1"); estado != http.StatusTooManyRequests {
			t.Fatalf("la segunda petición de la misma IP devolvió %d, se esperaba 429", estado)
		}
		if estado := enviarConXFF(t, e, "2.2.2.2"); estado != http.StatusCreated {
			t.Fatalf("una IP distinta detrás del proxy devolvió %d, debía tener su propio cubo", estado)
		}
	})
}

// --- Cabeceras -----------------------------------------------------------

func TestCabecerasDeSeguridad(t *testing.T) {
	e := montar(t)
	id := e.crear(t, bytes.Repeat([]byte("x"), 64))

	rutas := []string{"/", "/n/" + id, "/estatico/app.css", "/robots.txt"}

	esperadas := map[string]func(string) bool{
		"Cache-Control":          func(v string) bool { return strings.Contains(v, "no-store") },
		"X-Frame-Options":        func(v string) bool { return v == "DENY" },
		"Referrer-Policy":        func(v string) bool { return v == "no-referrer" },
		"X-Content-Type-Options": func(v string) bool { return v == "nosniff" },
		"X-Robots-Tag": func(v string) bool {
			return strings.Contains(v, "noindex") && strings.Contains(v, "nofollow")
		},
		"Content-Security-Policy": func(v string) bool {
			return strings.Contains(v, "default-src 'none'") &&
				strings.Contains(v, "frame-ancestors 'none'")
		},
	}

	for _, ruta := range rutas {
		t.Run(ruta, func(t *testing.T) {
			resp, err := http.Get(e.base + ruta)
			if err != nil {
				t.Fatalf("la petición falló: %v", err)
			}
			defer resp.Body.Close()
			for cabecera, valida := range esperadas {
				valor := resp.Header.Get(cabecera)
				if valor == "" {
					t.Errorf("falta la cabecera %s", cabecera)
					continue
				}
				if !valida(valor) {
					t.Errorf("la cabecera %s tiene un valor inesperado: %q", cabecera, valor)
				}
			}
		})
	}
}

func TestRobotsTxtProhibeTodo(t *testing.T) {
	e := montar(t)
	resp, err := http.Get(e.base + "/robots.txt")
	if err != nil {
		t.Fatalf("la petición falló: %v", err)
	}
	defer resp.Body.Close()
	cuerpo, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(cuerpo), "Disallow: /") {
		t.Errorf("robots.txt no prohíbe la indexación: %s", cuerpo)
	}
}

// --- Registro ------------------------------------------------------------

// TestElRegistroNoFiltraElIdentificador es la contracara de "nunca loguear el
// identificador": si apareciera en los registros, el archivo de logs sería una
// lista de notas pendientes de leer.
func TestElRegistroNoFiltraElIdentificador(t *testing.T) {
	e := montar(t)

	contenido := []byte("contenido cifrado que tampoco debe aparecer")
	id := e.crear(t, contenido)

	// Recorrer todas las rutas que llevan el identificador.
	resp, err := http.Get(e.base + "/n/" + id)
	if err != nil {
		t.Fatalf("la petición falló: %v", err)
	}
	resp.Body.Close()
	e.consumir(t, id)
	e.consumir(t, id) // el segundo intento, ya fallido

	registros := e.logs.String()
	if registros == "" {
		t.Fatal("no se registró ninguna petición: la prueba no verificaría nada")
	}
	if strings.Contains(registros, id) {
		t.Errorf("el identificador apareció en los registros:\n%s", registros)
	}
	if strings.Contains(registros, string(contenido)) {
		t.Errorf("el contenido de la nota apareció en los registros:\n%s", registros)
	}
	if strings.Contains(registros, base64.RawURLEncoding.EncodeToString(contenido)) {
		t.Errorf("el contenido cifrado apareció en los registros:\n%s", registros)
	}

	// Y sí debe quedar constancia de la petición, en forma genérica.
	if !strings.Contains(registros, "/n/{id}") {
		t.Errorf("no se registró la ruta genérica de la página de nota:\n%s", registros)
	}
	if !strings.Contains(registros, "/api/notas/{id}/consumir") {
		t.Errorf("no se registró la ruta genérica del endpoint de consumo:\n%s", registros)
	}
}

func TestEtiquetaRuta(t *testing.T) {
	casos := map[string]string{
		"/":                            "/",
		"/n/AbCdEfGhIjKlMnOpQrStUvWx":  "/n/{id}",
		"/api/notas":                   "/api/notas",
		"/api/notas/AbCdEf/consumir":   "/api/notas/{id}/consumir",
		"/estatico/app.css":            "/estatico/*",
		"/estatico/cripto.js":          "/estatico/*",
		"/robots.txt":                  "/robots.txt",
		"/salud":                       "/salud",
		"/cualquier/otra/cosa/AbCdEfG": "(otra)",
	}

	for ruta, esperada := range casos {
		if obtenida := etiquetaRuta(ruta); obtenida != esperada {
			t.Errorf("etiquetaRuta(%q) = %q, se esperaba %q", ruta, obtenida, esperada)
		}
	}
}

// TestEtiquetaRutaNuncaDevuelveElIdentificador prueba la propiedad, no los
// casos: sea cual sea la ruta, la etiqueta no puede contener el identificador.
func TestEtiquetaRutaNuncaDevuelveElIdentificador(t *testing.T) {
	for range 200 {
		id, err := nuevoID()
		if err != nil {
			t.Fatalf("no se pudo generar un identificador: %v", err)
		}
		rutas := []string{
			"/n/" + id,
			"/api/notas/" + id + "/consumir",
			"/estatico/" + id,
			"/" + id,
			"/algo/raro/" + id + "?x=1",
		}
		for _, ruta := range rutas {
			if etiqueta := etiquetaRuta(ruta); strings.Contains(etiqueta, id) {
				t.Fatalf("etiquetaRuta(%q) devolvió %q, que contiene el identificador", ruta, etiqueta)
			}
		}
	}
}

// --- Identificadores -----------------------------------------------------

func TestNuevoIDTieneLaFormaYLaEntropiaEsperadas(t *testing.T) {
	const cuantos = 5000
	vistos := make(map[string]struct{}, cuantos)

	for range cuantos {
		id, err := nuevoID()
		if err != nil {
			t.Fatalf("nuevoID devolvió error: %v", err)
		}
		if len(id) != largoIDTexto {
			t.Fatalf("el identificador %q mide %d caracteres, se esperaban %d",
				id, len(id), largoIDTexto)
		}
		if !identificadorValido(id) {
			t.Fatalf("el identificador generado %q no pasa su propia validación", id)
		}
		if _, repetido := vistos[id]; repetido {
			t.Fatalf("se repitió un identificador en %d generaciones: %q", cuantos, id)
		}
		vistos[id] = struct{}{}
	}

	// 18 bytes son 144 bits, por encima de los 128 exigidos.
	if bits := largoIDBytes * 8; bits < 128 {
		t.Fatalf("el identificador tiene %d bits de entropía, se exigen al menos 128", bits)
	}
}

func TestIdentificadorValido(t *testing.T) {
	valido, err := nuevoID()
	if err != nil {
		t.Fatalf("no se pudo generar un identificador: %v", err)
	}

	casos := map[string]bool{
		valido:                        true,
		"":                            false,
		"corto":                       false,
		valido + "x":                  false,
		valido[:largoIDTexto-1]:       false,
		strings.Repeat("A", 23):       false,
		strings.Repeat("A", 24):       true,
		strings.Repeat("-", 24):       true,
		strings.Repeat("_", 24):       true,
		strings.Repeat("A", 23) + "/": false,
		strings.Repeat("A", 23) + "+": false,
		strings.Repeat("A", 23) + ".": false,
		"../../../etc/passwd12345":    false,
	}

	for id, esperado := range casos {
		if obtenido := identificadorValido(id); obtenido != esperado {
			t.Errorf("identificadorValido(%q) = %v, se esperaba %v", id, obtenido, esperado)
		}
	}
}

// --- Otros ---------------------------------------------------------------

// TestElServidorGuardaElContenidoTalCualLoRecibe confirma que el servidor es
// un depósito opaco: devuelve exactamente los bytes que le entregaron, sin
// interpretarlos.
func TestElServidorGuardaElContenidoTalCualLoRecibe(t *testing.T) {
	e := montar(t)

	// Bytes arbitrarios, incluidos los que no son texto válido.
	original := make([]byte, 512)
	for i := range original {
		original[i] = byte(i % 256)
	}

	id := e.crear(t, original)
	resp, cuerpo := e.consumir(t, id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("el consumo devolvió %d", resp.StatusCode)
	}
	if leido := contenidoDe(t, cuerpo); !bytes.Equal(leido, original) {
		t.Error("el contenido devuelto no es idéntico byte a byte al enviado")
	}
}

func TestLaPaginaDeInicioLlevaElLimiteConfigurado(t *testing.T) {
	const limite = 4096
	e := montar(t, func(o *opcionesPrueba) { o.maxBytesNota = limite })

	resp, err := http.Get(e.base + "/")
	if err != nil {
		t.Fatalf("la petición falló: %v", err)
	}
	defer resp.Body.Close()
	cuerpo, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(cuerpo, []byte(fmt.Sprintf(`data-limite-bytes="%d"`, limite))) {
		t.Errorf("la página de inicio no lleva inyectado el límite del servidor")
	}
}

func TestSalud(t *testing.T) {
	e := montar(t)
	resp, err := http.Get(e.base + "/salud")
	if err != nil {
		t.Fatalf("la petición falló: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("salud devolvió %d, se esperaba 200", resp.StatusCode)
	}
}

func TestLimiteDeCuerpo(t *testing.T) {
	// El cuerpo permitido tiene que dar lugar al texto en claro, más lo que
	// agrega el cifrado, más lo que agrega base64.
	for _, maxNota := range []int{1, 1024, 100 * 1024} {
		limite := limiteDeCuerpo(maxNota)
		minimoNecesario := int64((maxNota+sobrecargaCifrado+2)/3*4) + 20
		if limite < minimoNecesario {
			t.Errorf("limiteDeCuerpo(%d) = %d, no alcanza para el mínimo necesario (%d)",
				maxNota, limite, minimoNecesario)
		}
	}
}

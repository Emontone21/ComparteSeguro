package servidor

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// carpetaDeMarca arma una carpeta de marca con los archivos indicados.
func carpetaDeMarca(t *testing.T, archivos map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for nombre, contenido := range archivos {
		if err := os.WriteFile(filepath.Join(dir, nombre), contenido, 0o644); err != nil {
			t.Fatalf("no se pudo escribir %s: %v", nombre, err)
		}
	}
	return dir
}

func obtener(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("la petición a %s falló: %v", url, err)
	}
	defer resp.Body.Close()
	cuerpo, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("no se pudo leer la respuesta: %v", err)
	}
	return resp, cuerpo
}

// TestSinMarcaSeMuestraElNombreEnTexto comprueba que la aplicación arranca y se
// ve entera aunque todavía no le hayan cargado el logo oficial.
func TestSinMarcaSeMuestraElNombreEnTexto(t *testing.T) {
	e := montar(t) // sin directorioMarca

	for _, ruta := range []string{"/", "/n/" + strings.Repeat("A", largoIDTexto)} {
		_, cuerpo := obtener(t, e.base+ruta)
		if !bytes.Contains(cuerpo, []byte(`class="logo-texto"`)) {
			t.Errorf("%s: sin logo cargado debía mostrarse el nombre en texto", ruta)
		}
		if bytes.Contains(cuerpo, []byte(`class="logo"`)) {
			t.Errorf("%s: no debía dibujarse una etiqueta de imagen sin logo cargado", ruta)
		}
		if !bytes.Contains(cuerpo, []byte("UTE")) {
			t.Errorf("%s: no aparece el nombre de la organización", ruta)
		}
	}

	// Y la hoja de marca tiene que decir explícitamente que no hay foto, para
	// que la capa de fondo no pinte nada.
	resp, cuerpo := obtener(t, e.base+"/marca/marca.css")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("marca.css devolvió %d", resp.StatusCode)
	}
	if !bytes.Contains(cuerpo, []byte("--foto-institucional:none")) {
		t.Errorf("marca.css debía declarar la foto como none, dice: %s", cuerpo)
	}
}

// TestLaCarpetaDelOperadorMandaSobreLoEmbebido es la prueba de la promesa
// central del sistema de marca: dejar los archivos oficiales en una carpeta y
// reiniciar alcanza, sin recompilar nada.
func TestLaCarpetaDelOperadorMandaSobreLoEmbebido(t *testing.T) {
	logo := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"></svg>`)
	foto := []byte("\xff\xd8\xff\xe0 esto hace de JPEG")

	e := montar(t, func(o *opcionesPrueba) {
		o.directorioMarca = carpetaDeMarca(t, map[string][]byte{
			"logo.svg":  logo,
			"fondo.jpg": foto,
		})
	})

	// La página ahora dibuja el logo en lugar del nombre en texto.
	_, pagina := obtener(t, e.base+"/")
	if !bytes.Contains(pagina, []byte(`src="/marca/logo.svg"`)) {
		t.Error("la página no apunta al logo cargado")
	}
	if bytes.Contains(pagina, []byte(`class="logo-texto"`)) {
		t.Error("con el logo cargado no debía mostrarse además el nombre en texto")
	}

	// Y los archivos se sirven tal cual, con su tipo de contenido.
	casos := []struct {
		ruta      string
		contenido []byte
		tipo      string
	}{
		{"/marca/logo.svg", logo, "image/svg+xml"},
		{"/marca/fondo.jpg", foto, "image/jpeg"},
	}
	for _, caso := range casos {
		resp, cuerpo := obtener(t, e.base+caso.ruta)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s devolvió %d", caso.ruta, resp.StatusCode)
			continue
		}
		if !bytes.Equal(cuerpo, caso.contenido) {
			t.Errorf("%s no devolvió el archivo tal cual se dejó", caso.ruta)
		}
		if tipo := resp.Header.Get("Content-Type"); tipo != caso.tipo {
			t.Errorf("%s tiene Content-Type %q, se esperaba %q", caso.ruta, tipo, caso.tipo)
		}
	}

	// La hoja de marca tiene que apuntar a la foto.
	_, css := obtener(t, e.base+"/marca/marca.css")
	if !bytes.Contains(css, []byte(`url("/marca/fondo.jpg")`)) {
		t.Errorf("marca.css no apunta a la foto cargada, dice: %s", css)
	}
}

func TestSeRespetaElOrdenDePreferenciaDeFormatos(t *testing.T) {
	e := montar(t, func(o *opcionesPrueba) {
		o.directorioMarca = carpetaDeMarca(t, map[string][]byte{
			"logo.png": []byte("png"),
			"logo.svg": []byte("svg"),
		})
	})

	_, pagina := obtener(t, e.base+"/")
	if !bytes.Contains(pagina, []byte(`src="/marca/logo.svg"`)) {
		t.Error("teniendo las dos, debía preferirse la versión vectorial")
	}
}

func TestSeIgnoraUnArchivoDeMarcaDemasiadoGrande(t *testing.T) {
	e := montar(t, func(o *opcionesPrueba) {
		o.directorioMarca = carpetaDeMarca(t, map[string][]byte{
			"fondo.jpg": bytes.Repeat([]byte("x"), topeArchivoDeMarca+1),
		})
	})

	_, css := obtener(t, e.base+"/marca/marca.css")
	if !bytes.Contains(css, []byte("--foto-institucional:none")) {
		t.Errorf("un archivo por encima del tope debía ignorarse, marca.css dice: %s", css)
	}
	if resp, _ := obtener(t, e.base+"/marca/fondo.jpg"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("un archivo ignorado no debía servirse, devolvió %d", resp.StatusCode)
	}
}

// TestNoSeSirveNadaFueraDeLasRanurasDeMarca cubre el recorrido de rutas: la
// búsqueda es una consulta exacta a un mapa armado al arrancar, así que ningún
// nombre disfrazado puede sacar un archivo de otro lado del disco.
func TestNoSeSirveNadaFueraDeLasRanurasDeMarca(t *testing.T) {
	dir := carpetaDeMarca(t, map[string][]byte{
		"logo.svg":    []byte("<svg/>"),
		"secreto.txt": []byte("esto no se sirve"),
	})
	e := montar(t, func(o *opcionesPrueba) { o.directorioMarca = dir })

	hostiles := []string{
		"/marca/secreto.txt",
		"/marca/..%2f..%2f..%2fetc%2fpasswd",
		"/marca/%2e%2e%2fmarca.go",
		"/marca/logo.svg.bak",
		"/marca/LOGO.SVG",
		"/marca/",
	}

	for _, ruta := range hostiles {
		resp, _ := obtener(t, e.base+ruta)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s devolvió 200 y no debía servir nada", ruta)
		}
	}

	// Y lo legítimo sigue funcionando.
	if resp, _ := obtener(t, e.base+"/marca/logo.svg"); resp.StatusCode != http.StatusOK {
		t.Errorf("el logo legítimo devolvió %d", resp.StatusCode)
	}
}

func TestLaMarcaSeConfiguraPorEntorno(t *testing.T) {
	e := montar(t, func(o *opcionesPrueba) {
		o.organizacion = "Otra Empresa"
		o.ubicacion = "Salto, Uruguay"
	})

	_, pagina := obtener(t, e.base+"/")
	for _, esperado := range []string{"Otra Empresa", "Salto, Uruguay"} {
		if !bytes.Contains(pagina, []byte(esperado)) {
			t.Errorf("la página no muestra %q", esperado)
		}
	}
}

// TestLosRecursosDeMarcaNoSeCachean confirma que las cabeceras de seguridad
// también alcanzan a los archivos de marca.
func TestLosRecursosDeMarcaNoSeCachean(t *testing.T) {
	e := montar(t, func(o *opcionesPrueba) {
		o.directorioMarca = carpetaDeMarca(t, map[string][]byte{"logo.svg": []byte("<svg/>")})
	})

	for _, ruta := range []string{"/marca/logo.svg", "/marca/marca.css"} {
		resp, _ := obtener(t, e.base+ruta)
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control = %q, se esperaba no-store", ruta, cc)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: falta X-Content-Type-Options: nosniff", ruta)
		}
	}
}

func TestLaEtiquetaDeRutaDeMarcaEsGenerica(t *testing.T) {
	casos := map[string]string{
		"/marca/logo.svg":  "/marca/*",
		"/marca/fondo.jpg": "/marca/*",
		"/marca/marca.css": "/marca/*",
	}
	for ruta, esperada := range casos {
		if obtenida := etiquetaRuta(ruta); obtenida != esperada {
			t.Errorf("etiquetaRuta(%q) = %q, se esperaba %q", ruta, obtenida, esperada)
		}
	}
}

func TestHojaDeMarca(t *testing.T) {
	casos := []struct {
		nombre   string
		marca    Marca
		contiene string
	}{
		{"sin foto", Marca{}, "--foto-institucional:none"},
		{"con foto", Marca{RutaFondo: "/marca/fondo.jpg"}, `url("/marca/fondo.jpg")`},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			if hoja := string(hojaDeMarca(caso.marca)); !strings.Contains(hoja, caso.contiene) {
				t.Errorf("hojaDeMarca() = %q, debía contener %q", hoja, caso.contiene)
			}
		})
	}
}

// TestElNombreDeLaOrganizacionSeEscapa evita que un nombre con caracteres
// especiales pueda inyectar marcado en la página.
func TestElNombreDeLaOrganizacionSeEscapa(t *testing.T) {
	e := montar(t, func(o *opcionesPrueba) {
		o.organizacion = `<script>alert(1)</script>`
	})

	_, pagina := obtener(t, e.base+"/")
	if bytes.Contains(pagina, []byte("<script>alert(1)</script>")) {
		t.Error("el nombre de la organización se insertó sin escapar")
	}
	if !bytes.Contains(pagina, []byte("&lt;script&gt;")) {
		t.Errorf("se esperaba el nombre escapado en la página")
	}
}

# Marca institucional

**Dejá acá el logo y la foto oficiales.** Esta carpeta se monta dentro del
contenedor y la aplicación la lee al arrancar. No hay que recompilar nada ni
tener Go instalado: se copian los archivos, se reinicia el contenedor y listo.

```bash
cp /donde/tengas/logo.svg   marca/logo.svg
cp /donde/tengas/edificio.jpg marca/fondo.jpg
docker compose restart
```

## Las dos ranuras

| Archivo | Dónde aparece | Formatos aceptados, por orden de preferencia |
|---|---|---|
| `logo.*`  | Arriba a la izquierda, en las dos páginas | `logo.svg`, `logo.png`, `logo.webp`, `logo.jpg` |
| `fondo.*` | Fondo tenue y fijo de toda la página | `fondo.jpg`, `fondo.png`, `fondo.webp`, `fondo.svg` |

Si están las dos versiones de una ranura, gana la primera de la lista. Si una
ranura queda vacía, la aplicación se ve igual de terminada: sin logo muestra el
nombre de la organización en texto, y sin foto el fondo queda liso.

## Lo que hay hoy en esta carpeta

| Archivo | Qué es |
|---|---|
| `logo.png` | El logotipo, recortado y con fondo transparente. Es el que usa la aplicación. |
| `fondo.jpg` | La fotografía del Palacio de la Luz, reducida para la web. Es la que usa la aplicación. |
| `originales/` | Los archivos tal como se subieron, sin tocar. |

Los dos archivos que usa la aplicación se derivaron de los originales, sin
alterar la imagen en sí:

- **`logo.png`** sale de `originales/logo.jpg`, que era un cuadrado de 512×512
  donde el logotipo ocupaba apenas 377×172 en el centro y el resto era blanco.
  A 38 píxeles de alto se habría visto de 13. Se recortó al contorno del
  logotipo y se pasó a PNG con el blanco convertido en transparencia, porque
  un JPEG habría dejado un rectángulo blanco en el modo oscuro. Los colores
  del logotipo no se modificaron.
- **`fondo.jpg`** sale de `originales/edificio.jpg`, de 2904×2008 y 618 KB,
  reducido a 1920 píxeles de ancho y 348 KB. La aplicación pide no cachear
  nada, así que la foto se descarga en cada visita; a la resolución en que se
  muestra, detrás del velo, la diferencia no se ve.

Si conseguís el logotipo oficial en vectorial, dejalo como `logo.svg`: gana
sobre el PNG automáticamente y no hace falta ninguna preparación.

## Cómo preparar los archivos

**El logo.** Recortado a su propio contorno, sin márgenes alrededor: la
aplicación lo dibuja a 38 píxeles de alto, así que un archivo cuadrado con el
logotipo chiquito en el medio se va a ver diminuto. En vectorial (`.svg`) es lo
ideal porque se ve nítido en cualquier pantalla; si es `.png`, que tenga al
menos 120 píxeles de alto y fondo transparente.

**La foto.** Apaisada, de 1600 píxeles de ancho o más, y por debajo de 500 KB.
Conviene una toma donde el motivo esté hacia el centro, porque en pantallas
angostas se recortan los bordes. No hace falta que la aclares ni le bajes el
contraste: la aplicación le tiende encima un velo del color de la página, así
que va a quedar tenue sí o sí y el texto siempre se va a leer.

El tope por archivo es de 8 MB. Uno más grande se ignora y queda anotado en el
registro al arrancar.

## Nombre y ubicación de la organización

No son archivos, son variables de entorno en el `docker-compose.yml`:

```yaml
ORG_NAME: "UTE"
ORG_LOCATION: "Montevideo, Uruguay"
```

## Comprobar que se tomaron

Al arrancar, el registro dice qué encontró:

```
docker compose logs comparteseguro | grep marca
```

```
msg="recurso de marca tomado de la carpeta del operador" ranura=logo archivo=logo.svg
msg="recurso de marca tomado de la carpeta del operador" ranura=fondo archivo=fondo.jpg
```

Si una ranura no aparece en el registro, el archivo no estaba, tenía un nombre
distinto a los de la tabla, o superaba el tope de tamaño.

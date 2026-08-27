# Comparte Seguro

Aplicación web interna para compartir contraseñas y notas sensibles mediante un
enlace de un solo uso. El destinatario abre el enlace, ve el contenido una única
vez, y a partir de ese momento el enlace queda inutilizado para siempre.

El servidor guarda únicamente texto cifrado: no puede leer las notas ni bajo
orden judicial, porque no tiene la clave.

```
docker compose up -d --build
```

---

## Índice

- [El stack y por qué](#el-stack-y-por-qué)
- [Decisiones de diseño](#decisiones-de-diseño)
- [Modelo de seguridad, en lenguaje llano](#modelo-de-seguridad-en-lenguaje-llano)
- [Requisito ineludible: HTTPS](#requisito-ineludible-https)
- [Despliegue](#despliegue)
- [Configuración](#configuración)
- [Operación](#operación)
- [Tests](#tests)
- [Estructura del proyecto](#estructura-del-proyecto)

---

## El stack y por qué

**Go con su biblioteca estándar, SQLite mediante un driver en Go puro, y un
frontend sin framework.**

Las prioridades planteadas eran pocas dependencias, tecnología madura, y
facilidad de despliegue on-premise. Cada una empujó en la misma dirección.

### Pocas dependencias

El proyecto tiene **una sola dependencia directa**: `modernc.org/sqlite`. Todo
lo demás —el servidor HTTP, el ruteo, el generador de números aleatorios
criptográfico, el manejo de JSON, el registro de eventos y el framework de
tests— viene con Go.

El frontend son tres archivos de JavaScript y una hoja de estilos. Sin npm, sin
paso de compilación, sin `node_modules`. El cifrado lo hace WebCrypto, que ya
está en el navegador.

Esto importa más que de costumbre en esta aplicación en particular: se trata de
una herramienta para manejar secretos, y cada dependencia es código de terceros
que hay que confiar y auditar. La superficie a revisar acá cabe en una tarde.

### Tecnología madura

`net/http` y `crypto/rand` son de lo más ejercitado del ecosistema Go. SQLite
es probablemente el motor de base de datos más desplegado del mundo.

Sobre el driver hubo que elegir. El más conocido, `mattn/go-sqlite3`, envuelve
la biblioteca original en C, lo que obliga a compilar con cgo: hace falta un
compilador de C en la imagen y el binario resultante queda atado a una
implementación concreta de libc. `modernc.org/sqlite` es **el mismo SQLite**,
transpilado a Go, y compila sin cgo. Se gana un binario estático y un
Dockerfile sin complicaciones; no se pierde nada, porque el motor es el mismo.

### Despliegue on-premise

El resultado es un binario estático único. La imagen final es Alpine más ese
binario: unos 20 MB, sin intérprete, sin entorno virtual, sin `node_modules`.
Las páginas HTML, el CSS y el JavaScript van **embebidos dentro del binario**,
así que no hay archivos sueltos que sincronizar ni volúmenes de solo lectura que
montar. El único estado que vive fuera del contenedor es el archivo SQLite.

### Lo que se descartó

- **Node + Express**: el árbol de dependencias transitivas es de otro orden de
  magnitud. Para una aplicación cuyo propósito es reducir la superficie de
  confianza, es ir en contra.
- **Python + Flask**: necesita además un servidor WSGI de producción, y la
  imagen con el intérprete pesa varias veces más. Nada de eso es grave, pero no
  aporta nada frente a la alternativa.

---

## Decisiones de diseño

### El borrado es una sola operación, no dos

Es el corazón de la aplicación. Si entregar la nota y borrarla fueran dos pasos
—leerla, después borrarla— existiría una ventana entre ambos: dos peticiones
simultáneas al mismo enlace podrían leer las dos el contenido antes de que
alguna llegara a borrarlo. Con secretos, esa ventana es exactamente el agujero
que hay que evitar.

Acá no son dos pasos. Es una sola sentencia SQL:

```sql
DELETE FROM notas WHERE id = ? RETURNING contenido
```

SQLite la ejecuta de forma indivisible, dentro de una transacción que toma el
bloqueo de escritura desde el principio. Si una petición llega a ver el
contenido, es porque ninguna otra lo vio ni lo va a ver. Está verificado con
tests que lanzan cientos de peticiones simultáneas contra la misma nota y
comprueban que **exactamente una** se lleva el contenido.

### Revelar la nota es un POST, nunca un GET

Abrir el enlace muestra una pantalla intermedia. Recién al presionar "Ver nota"
se pide el contenido, y se pide con `POST`.

No es un capricho de purismo REST. Los clientes de correo, las plataformas de
mensajería y varios antivirus corporativos **visitan automáticamente** los
enlaces que reciben, para generar vistas previas o para analizarlos. Si revelar
la nota fuera un `GET`, el escáner de Outlook o de Slack quemaría la nota antes
de que el destinatario llegara a abrirla, y este se encontraría con un enlace
muerto.

Con `POST`, la pantalla intermedia deja de ser un adorno: es la defensa. Solo un
clic humano destruye la nota.

### El servidor nunca distingue "ya leída" de "no existe"

Un identificador ya consumido, uno que nunca existió y uno con formato inválido
devuelven los tres exactamente la misma respuesta, byte por byte. Si se
distinguieran, alguien que probara identificadores al azar podría averiguar
cuáles llegaron a existir alguna vez.

Por el mismo motivo, abrir `/n/{id}` muestra siempre la pantalla intermedia sin
consultar la base: si respondiera distinto según la nota existiera o no,
bastaría con visitar una URL para saber si hay una nota esperando.

### Los identificadores no aparecen en los registros

El registro anota la forma genérica de la ruta (`/n/{id}`), nunca la ruta real.
Un archivo de logs que guardara los identificadores sería, en la práctica, una
lista de notas pendientes de leer, servida a cualquiera que tenga acceso a los
logs. Hay un test que genera notas, las recorre por todas sus rutas y verifica
que ni el identificador ni el contenido aparecen en el registro.

### La clave se valida antes de destruir la nota

Al presionar "Ver nota", el navegador primero comprueba que la clave del enlace
sea válida y recién después le pide la nota al servidor. Los clientes de correo
suelen cortar las URL largas, y un enlace truncado no sirve para descifrar
nada: más vale avisar antes que destruir la nota y no poder mostrarla.

### Sin expiración por tiempo

Se dejó fuera a propósito, como se pidió. Vale saber la consecuencia: una nota
que nadie abre **queda guardada indefinidamente**. Como está cifrada y el
servidor no tiene la clave, el riesgo es acotado, pero conviene revisar de vez
en cuando cuántas se acumulan (ver [Operación](#operación)).

---

## Modelo de seguridad, en lenguaje llano

*Esta sección está escrita para el equipo de IT. No hace falta saber
criptografía para leerla.*

### Qué pasa cuando alguien crea una nota

1. La persona escribe la nota en el navegador y presiona "Generar enlace".
2. **El navegador genera una clave al azar y cifra la nota ahí mismo**, antes de
   mandar nada. El servidor no participa de esto.
3. El navegador le manda al servidor únicamente el texto ya cifrado. El servidor
   le asigna un identificador aleatorio y lo guarda.
4. El navegador arma el enlace final:

```
https://comparte.empresa.local/n/kJ8mQx2vT9pL4nR7wY3zA5bC#Xq7mR2nP...
                                 └──── identificador ────┘ └── clave ──┘
                                    esto sí lo ve el servidor    esto NO
```

### Por qué la clave no llega al servidor

Todo lo que va después del signo `#` en una dirección web se llama
**fragmento**. Los navegadores lo usan para saltar a una sección dentro de una
página, y —esto es lo importante— **nunca lo incluyen en el pedido que le hacen
al servidor**. Es una regla del propio protocolo web, no una decisión de esta
aplicación.

O sea: la clave que descifra la nota vive en la mitad del enlace que el servidor
nunca recibe. El servidor guarda un texto cifrado que no tiene con qué abrir.

*(Esto está verificado: hay una prueba automatizada que maneja un navegador de
verdad, registra todas las peticiones que hace, y comprueba que la clave no
aparece en ninguna.)*

### Qué pasa cuando alguien lee la nota

1. Abre el enlace y ve una pantalla que le avisa que la nota se destruirá.
2. Al presionar "Ver nota", el servidor **entrega el texto cifrado y lo borra en
   la misma operación**. No hay forma de que dos personas lean la misma nota.
3. El navegador descifra la nota con la clave que venía en el enlace y la
   muestra.
4. Cualquier acceso posterior a ese enlace dice "Esta nota ya fue leída o no
   existe".

### De qué protege esto

- **De alguien con acceso a la base de datos.** Solo va a encontrar texto
  cifrado sin las claves.
- **De alguien con acceso a los respaldos.** Lo mismo.
- **De alguien con acceso a los registros del servidor.** No contienen ni el
  contenido, ni los identificadores, ni las URL completas.
- **De que un secreto quede dando vueltas.** El principal problema de mandar una
  contraseña por chat o por correo es que se queda ahí para siempre, en el
  historial de ambas partes y en los respaldos del servidor de correo. Acá, una
  vez leída, no queda nada.
- **De que alguien haya leído la nota sin que nos enteremos.** Si el destinatario
  reporta que el enlace ya estaba usado, eso es una señal de alarma clara: hay
  que rotar el secreto.
- **De los buscadores y los archivadores.** Las páginas van marcadas como no
  indexables y con la orden de no guardarse en caché.

### De qué NO protege

Esto es tan importante como lo anterior. Conviene que el equipo lo tenga claro:

- **De quien tenga el enlace completo antes que el destinatario.** El enlace *es*
  la credencial. Quien lo consiga, lee la nota. Por eso importa el canal por el
  que se manda.
- **Del canal por el que se comparte el enlace.** Si se manda la contraseña por
  WhatsApp *y* el enlace por WhatsApp, no se ganó gran cosa: el enlace queda en
  el historial de WhatsApp igual. La ganancia aparece cuando el enlace se
  destruye al leerse, cosa que sí ocurre, y cuando el canal del enlace es
  distinto del canal donde vive el secreto.
- **De un navegador o una computadora comprometidos.** Si la máquina de alguna de
  las dos puntas está infectada, no hay cifrado que ayude.
- **De alguien que espíe la red, si no se usa HTTPS.** Ver la sección siguiente.
- **De alguien de la red interna que quiera crear notas.** No hay login: es lo
  que se pidió. Cualquiera con acceso a la red puede usar la aplicación.
- **No confirma quién leyó la nota.** Si el enlace figura como usado y el
  destinatario dice que nunca lo abrió, hay que asumir que el secreto se filtró
  y rotarlo.

### La regla práctica para el equipo

> Si el destinatario dice que el enlace ya estaba usado, **el secreto está
> comprometido**. Rotalo. No lo reenvíes.

### Las medidas técnicas, en una lista

| Medida | Qué hace |
|---|---|
| Cifrado AES-256-GCM en el navegador | El servidor nunca ve el contenido en claro. GCM además detecta si el texto cifrado fue alterado. |
| Clave en el fragmento de la URL | La clave no llega al servidor por diseño del protocolo web. |
| Identificadores de 144 bits | Generados con el generador criptográfico del sistema. Adivinar uno es inviable. |
| Borrado atómico | Entregar y borrar son la misma operación. Dos lecturas simultáneas nunca ganan las dos. |
| Revelar es POST, no GET | Los escáneres de enlaces no pueden quemar la nota. |
| Respuesta única ante el fallo | No se distingue "ya leída" de "no existe" de "identificador inválido". |
| Registros sin identificadores | Los logs no sirven para encontrar notas pendientes. |
| `Cache-Control: no-store` | Ni el navegador ni los proxies intermedios guardan copia. |
| `X-Frame-Options: DENY` | La aplicación no se puede incrustar en otra página para engañar a alguien. |
| `Referrer-Policy: no-referrer` | El enlace no se filtra al navegar hacia afuera. |
| Política de seguridad de contenido restrictiva | La página no carga nada de terceros ni ejecuta código en línea. |
| `noindex, nofollow` y `robots.txt` | Los buscadores no indexan nada. |
| Límite de peticiones por IP | Acota la creación masiva de notas. |
| Límite de tamaño | 100 KB por nota, validado en el servidor. |

---

## Requisito ineludible: HTTPS

**La aplicación no funciona sobre HTTP simple**, salvo en `localhost`.

No es una restricción de esta aplicación: los navegadores solo habilitan
WebCrypto —el motor de cifrado que hace todo el trabajo— en lo que llaman
*contextos seguros*, o sea HTTPS y `localhost`. Sobre HTTP simple la aplicación
muestra un cartel explicando el problema y no deja crear ni leer notas.

Además de eso, sin HTTPS el enlace viajaría en claro por la red interna: la
clave está en el fragmento y no llega al servidor, pero sí atraviesa la red
dentro del mensaje que la comparte, y cualquiera que escuche el tráfico podría
capturarlo.

**En resumen: hay que poner un proxy inverso con TLS delante.** Un certificado
de la autoridad certificadora interna de la empresa alcanza perfectamente. Ver
el ejemplo en [Despliegue](#despliegue).

---

## Despliegue

### Requisitos

- Docker con el plugin `compose`.
- Un certificado TLS para el nombre interno que se le vaya a dar.

### Puesta en marcha

```bash
git clone <url-del-repositorio> comparteseguro
cd comparteseguro
docker compose up -d --build
```

Listo. La aplicación queda escuchando en el puerto 8080 y las notas se guardan
en un volumen de Docker llamado `comparteseguro-datos`.

Para cambiar el puerto, o cualquier otra cosa, se crea un archivo `.env` al lado
del `docker-compose.yml`:

```bash
# .env
PORT=9000
RATE_LIMIT_PER_MINUTE=30
TRUST_PROXY=true
```

Y se vuelve a levantar:

```bash
docker compose up -d
```

### Comprobar que arrancó bien

```bash
curl -f http://localhost:8080/salud   # responde: ok
docker compose ps                     # el estado debe decir "healthy"
docker compose logs -f                # los registros
```

### Poner HTTPS delante

Cualquier proxy inverso sirve. Con nginx:

```nginx
server {
    listen 443 ssl;
    server_name comparte.empresa.local;

    ssl_certificate     /etc/ssl/certs/comparte.empresa.local.crt;
    ssl_certificate_key /etc/ssl/private/comparte.empresa.local.key;

    # Las notas pueden llegar hasta 100 KB, y crecen un tercio al codificarse.
    client_max_body_size 1m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

# Redirigir el tráfico sin cifrar.
server {
    listen 80;
    server_name comparte.empresa.local;
    return 301 https://$host$request_uri;
}
```

Con un proxy delante hay que poner `TRUST_PROXY=true`, para que el límite de
peticiones cuente la IP real del cliente y no la del proxy. **Y solo entonces**:
ver la advertencia en la sección siguiente.

### Actualizar

```bash
git pull
docker compose up -d --build
```

Las notas pendientes sobreviven a la actualización: están en el volumen, no en
la imagen.

---

## Configuración

Todo se configura con variables de entorno.

| Variable | Por omisión | Para qué sirve |
|---|---|---|
| `PORT` | `8080` | Puerto de escucha. |
| `BIND_ADDR` | `0.0.0.0` | Interfaz de escucha. |
| `DB_PATH` | `/datos/comparteseguro.db` | Archivo SQLite. Tiene que estar en el volumen. |
| `MAX_NOTE_BYTES` | `102400` | Tamaño máximo de una nota, en bytes de texto sin cifrar. Se valida en el servidor y también alimenta el contador que ve el usuario. |
| `RATE_LIMIT_PER_MINUTE` | `20` | Enlaces por minuto que puede crear una misma IP, de forma sostenida. |
| `RATE_LIMIT_BURST` | `10` | Ráfaga tolerada por encima de ese ritmo. |
| `TRUST_PROXY` | `false` | Si hay un proxy inverso de confianza delante. Ver la advertencia. |
| `LOG_FORMAT` | `texto` | `texto` o `json`. |

### Advertencia sobre `TRUST_PROXY`

Con `TRUST_PROXY=true` la aplicación cree lo que diga la cabecera
`X-Forwarded-For` para saber quién es el cliente.

**Ponelo en `true` únicamente si hay de verdad un proxy inverso delante y el
puerto de la aplicación no es accesible por otro camino.** Si la aplicación
queda expuesta directamente con esta opción activada, cualquiera puede inventarse
la cabecera, aparentar una IP distinta en cada pedido y saltearse el límite de
peticiones por completo.

Cuando está activada, la aplicación toma la **última** entrada de la cabecera,
que es la que agrega el propio proxy. Las anteriores las puede haber puesto el
cliente y no son confiables.

---

## Operación

### Dónde están las notas

En el volumen `comparteseguro-datos`, en un único archivo SQLite. Es todo el
estado que tiene la aplicación.

### Respaldos

En general **no hacen falta**. Lo que hay ahí son notas pendientes de leer, que
son efímeras por definición, y están cifradas con claves que el servidor no
tiene: un respaldo de esa base es un archivo de contenido inútil para quien lo
lea, y de valor nulo para restaurar.

Si aun así la política interna exige respaldar todos los volúmenes, no hay
inconveniente: no se filtra nada.

### Cuántas notas hay pendientes

```bash
docker compose exec comparteseguro wget -qO- http://127.0.0.1:8080/salud
```

El endpoint `/salud` confirma que la base responde. Como no hay expiración por
tiempo, si el archivo creciera mucho con el correr de los meses, se lo puede
vaciar sin más: son notas que nadie abrió.

### Vaciar las notas pendientes

Destruye todas las notas sin leer. Irreversible.

```bash
docker compose down
docker volume rm comparteseguro-datos
docker compose up -d
```

### Registros

```bash
docker compose logs -f comparteseguro
```

Se registra el método, la forma genérica de la ruta, el código de respuesta y la
IP. **No se registra nunca** el contenido de las notas, los identificadores ni
las URL completas. Una línea típica:

```
time=2026-08-27T11:47:53.037Z level=INFO msg=petición metodo=POST \
  ruta=/api/notas/{id}/consumir estado=404 bytes=48 ms=0 ip=10.20.1.44
```

---

## Tests

```bash
go test ./... -race
```

Cubren el camino crítico y las propiedades de seguridad que no se pueden
verificar leyendo el código:

**Camino crítico**
- Crear una nota, leerla una vez y comprobar que el segundo acceso falla.
- Abrir la pantalla intermedia no consume la nota.
- Un `GET` sobre el endpoint de lectura no destruye nada (devuelve 405).

**Concurrencia** — la propiedad central de la aplicación
- A nivel de almacén: 40 notas con 16 peticiones simultáneas cada una; se
  verifica que exactamente una gana por nota y que no queda ninguna sin borrar.
- A nivel HTTP: lo mismo sobre un servidor real, comprobando además que la
  ganadora recibe el contenido correcto.

**Propiedades de seguridad**
- Una nota ya leída, una inexistente y un identificador mal formado devuelven
  respuestas idénticas byte por byte.
- Los identificadores nunca aparecen en los registros (se generan, se recorren
  todas sus rutas y se inspecciona el registro completo).
- Los identificadores tienen la forma y la entropía esperadas, y no se repiten
  en 5000 generaciones.
- `X-Forwarded-For` se ignora salvo que se declare que hay un proxy de
  confianza; con la opción activa, cada IP tiene su propio cupo.
- Las cabeceras de seguridad están presentes en todas las rutas.

**Validación y límites**
- Se rechazan las notas que superan el límite, tanto las que lo pasan por poco
  como las que lo pasan por mucho.
- Se rechaza un texto cifrado imposiblemente corto, cuerpos mal formados y
  campos desconocidos.
- El límite de peticiones corta la ráfaga y devuelve `Retry-After`.

**Verificación en navegador real**

Los tests de Go no pueden ejercitar WebCrypto. Durante el desarrollo se verificó
el flujo completo manejando un Chromium de verdad: que el texto descifrado sea
idéntico al original (acentos y emoji incluidos), que la clave no aparezca en
ninguna de las peticiones que hace el navegador, que la pantalla intermedia no
consuma la nota, que el segundo acceso falle, y que la política de seguridad de
contenido no rompa nada.

---

## Estructura del proyecto

```
├── cmd/comparteseguro/       arranque, señales y apagado ordenado
├── internal/
│   ├── almacen/              SQLite: guardar y consumir de forma atómica
│   ├── config/               lectura y validación de variables de entorno
│   ├── ratelimit/            cubo de fichas por IP, en memoria
│   └── servidor/             rutas, middlewares y handlers
├── web/                      interfaz, embebida en el binario
│   ├── index.html            crear una nota
│   ├── nota.html             pantalla intermedia y contenido revelado
│   └── estatico/
│       ├── cripto.js         cifrado y descifrado con WebCrypto
│       ├── crear.js          pantalla de creación
│       ├── ver.js            pantalla de lectura
│       └── app.css           estilos
├── Dockerfile
└── docker-compose.yml
```

### Los endpoints

| Ruta | Qué hace |
|---|---|
| `GET /` | Pantalla de creación. |
| `GET /n/{id}` | Pantalla intermedia. **No consulta la base ni consume nada.** |
| `POST /api/notas` | Guarda una nota cifrada. Devuelve solo el identificador. |
| `POST /api/notas/{id}/consumir` | Entrega el contenido y lo borra, en una sola operación. |
| `GET /salud` | Comprobación de estado, para Docker y para monitoreo. |
| `GET /robots.txt` | Prohíbe la indexación. |

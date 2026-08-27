# --- Etapa de compilación -------------------------------------------------
#
# El driver de SQLite es Go puro, así que se compila con CGO_ENABLED=0 y sale
# un binario estático: no depende de ninguna biblioteca del sistema y no hace
# falta un compilador de C en ninguna etapa.
FROM golang:1.25-alpine AS compilacion

WORKDIR /origen

# Las dependencias primero, en su propia capa: mientras go.mod y go.sum no
# cambien, Docker reutiliza la descarga.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath saca las rutas de compilación del binario; -s -w le quitan la tabla
# de símbolos y la información de depuración, que no hacen falta en producción.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /salida/comparteseguro \
        ./cmd/comparteseguro


# --- Imagen final ----------------------------------------------------------
FROM alpine:3.21

# Usuario sin privilegios. El directorio de datos se crea acá y con su dueño
# puesto: cuando Docker monte encima un volumen con nombre, hereda estos
# permisos.
RUN addgroup -g 10001 -S comparte \
 && adduser  -u 10001 -S -G comparte comparte \
 && mkdir -p /datos \
 && chown comparte:comparte /datos

COPY --from=compilacion /salida/comparteseguro /usr/local/bin/comparteseguro

USER comparte:comparte

ENV PORT=8080 \
    DB_PATH=/datos/comparteseguro.db

EXPOSE 8080
VOLUME ["/datos"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --quiet --spider "http://127.0.0.1:${PORT}/salud" || exit 1

ENTRYPOINT ["/usr/local/bin/comparteseguro"]

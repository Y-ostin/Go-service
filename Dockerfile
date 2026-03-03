# ─────────────────────────────────────────────────────────────────────────────
# Etapa 1: Build — compilar el binario Go en un contenedor con las dependencias
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Instalar git (necesario para go get con módulos privados si aplica)
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copiar go.mod primero y resolver dependencias (incluye GORM)
COPY go.mod ./

# Descargar y actualizar go.sum para todas las dependencias (incluye gorm.io/*)
RUN go get gorm.io/gorm@v1.25.10 && \
    go get gorm.io/driver/mysql@v1.5.6

# Copiar el resto del código fuente
COPY . .

# Copiar archivos estáticos necesarios para el runtime desde su ubicación real
# El archivo dashboard.html está en una subcarpeta y lo queremos en la raíz /app
RUN cp internal/infrastructure/http/dashboard.html /app/dashboard.html

# Sincronizar go.sum con las dependencias reales del código
RUN go mod tidy

# Compilar con optimizaciones para producción:
#   CGO_ENABLED=0  → binario estático sin deps de C (compatible con scratch/alpine)
#   -ldflags="-w -s" → eliminar debug symbols (binario ~30% más pequeño)
#   -trimpath → eliminar rutas del sistema del binario
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-w -s -X main.version=1.0.0 -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -trimpath \
    -o /app/go-binlog-service \
    ./cmd/server

# ─────────────────────────────────────────────────────────────────────────────
# Etapa 2: Runtime — imagen mínima de producción
# ─────────────────────────────────────────────────────────────────────────────
FROM alpine:3.19

# Certificados TLS y zona horaria (necesarios para conexiones TLS y logs con timezone)
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S appgroup && \
    adduser -S appuser -G appgroup

WORKDIR /app

# Copiar el binario compilado, el dashboard estático y el CA cert de EMQX
COPY --from=builder /app/go-binlog-service .
COPY --from=builder /app/dashboard.html .
COPY --from=builder /app/emqxsl-ca.crt .

# Directorio para persistir la posición del binlog
RUN mkdir -p /data && chown appuser:appgroup /data

# Ejecutar como usuario no-root (principio de mínimo privilegio)
USER appuser

# Puerto del servidor HTTP
EXPOSE 8090

# Variables de entorno con valores por defecto seguros
ENV SERVER_PORT=8090 \
    LOG_LEVEL=info \
    LOG_FORMAT=json \
    POSITION_FILE_PATH=/data/binlog_position.json \
    BINLOG_FLAVOR=mariadb

# Health check para Docker/Kubernetes
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:${SERVER_PORT}/health || exit 1

ENTRYPOINT ["./go-binlog-service"]

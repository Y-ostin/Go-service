# go-binlog-service

Backend en Go que consume el Binary Log de MariaDB 10.4 en tiempo real y emite eventos al dashboard vía WebSocket y SSE.

## Arquitectura

```
MariaDB binlog → BinlogListener → EventDispatcher (worker pool + batcher) → WebSocket Hub / SSE Broker → Dashboard
```

## Requisitos previos

### MariaDB 10.4 — configuración requerida

Añadir a `my.cnf` o `my.ini`:

```ini
server-id       = 1
log-bin         = mysql-bin
binlog_format   = ROW
binlog_row_image = FULL
expire_logs_days = 7
```

Crear usuario de replicación:

```sql
CREATE USER 'replicator'@'%' IDENTIFIED BY 'tu_password';
GRANT REPLICATION SLAVE ON *.* TO 'replicator'@'%';
FLUSH PRIVILEGES;
```

Verificar configuración:

```sql
SHOW MASTER STATUS;
SHOW VARIABLES LIKE 'binlog_format';   -- debe ser ROW
SHOW VARIABLES LIKE 'server_id';       -- debe ser 1
```

## Inicio rápido (Docker Compose)

```bash
# 1. Copiar variables de entorno
cp .env.example .env
# Editar .env con las credenciales reales

# 2. Levantar stack completo (MariaDB + Go service + Prometheus + Grafana)
docker compose up --build -d

# 3. Ver logs del servicio
docker compose logs -f go-binlog-service

# 4. Verificar health
curl http://localhost:8090/health
```

## Inicio rápido (Go directo)

```bash
# Descargar dependencias
go mod download

# Configurar variables de entorno (o crear .env)
export DB_HOST=127.0.0.1
export DB_REPL_USER=replicator
export DB_REPL_PASSWORD=tu_password
export DB_SERVER_ID=100
export BINLOG_SCHEMAS=ccip_erp_legacy

# Ejecutar
go run ./cmd/server
```

## Endpoints

| Endpoint | Descripción |
|---|---|
| `ws://host:8090/ws` | WebSocket — connect con JavaScript `new WebSocket(...)` |
| `http://host:8090/events` | SSE — connect con JavaScript `new EventSource(...)` |
| `http://host:8090/health` | Health check JSON |
| `http://host:8090/metrics` | Métricas Prometheus |
| `http://host:8090/info` | Información del servicio |

## Integración en el Dashboard (Vue/React)

### Via SSE (recomendado para dashboards)

```javascript
const eventSource = new EventSource('http://localhost:8090/events');

eventSource.onmessage = (event) => {
  const binlogEvent = JSON.parse(event.data);
  console.log(binlogEvent);
  // {
  //   id: "550e8400-...",
  //   type: "INSERT",
  //   schema: "ccip_erp_legacy",
  //   table: "financial_results",
  //   after: { id: 1, amount: "5000.00", ... },
  //   timestamp: "2025-01-01T12:00:00Z"
  // }
};

eventSource.onerror = () => {
  // El browser reconecta automáticamente (retry: 3000ms configurado)
};
```

### Via WebSocket

```javascript
const ws = new WebSocket('ws://localhost:8090/ws');

ws.onmessage = (event) => {
  const binlogEvent = JSON.parse(event.data);
  // Mismo formato que SSE
};
```

## Métricas clave (Prometheus)

| Métrica | Descripción |
|---|---|
| `binlog_events_total` | Eventos procesados por tipo/schema/tabla |
| `binlog_lag_seconds` | Retraso entre evento en DB y procesamiento en Go |
| `binlog_reconnections_total` | Reconexiones al stream del binlog |
| `ws_connected_clients` | Clientes WebSocket/SSE conectados |
| `worker_queue_depth` | Eventos pendientes en el worker pool |
| `batches_sent_total` | Batches enviados al broadcast |

## Estructura del proyecto

```
GO-SERVICE/
├── cmd/server/main.go                    ← Punto de entrada (wire de dependencias)
├── internal/
│   ├── domain/
│   │   ├── event.go                      ← Entidades de dominio (BinlogEvent)
│   │   └── ports.go                      ← Puertos (interfaces) hex. architecture
│   ├── application/
│   │   └── dispatcher.go                 ← Worker pool + batcher + idempotencia
│   ├── infrastructure/
│   │   ├── binlog/
│   │   │   ├── listener.go               ← Consumidor del binlog (go-mysql)
│   │   │   └── position_store.go         ← Persistencia File+Position en disco
│   │   ├── websocket/hub.go              ← Hub WebSocket (gorilla/websocket)
│   │   ├── sse/broker.go                 ← Broker SSE nativo Go
│   │   └── http/server.go                ← Servidor HTTP con todos los handlers
│   └── config/config.go                  ← Configuración via env vars + viper
├── pkg/
│   ├── logger/logger.go                  ← Logger estructurado (zap)
│   └── metrics/metrics.go                ← Métricas Prometheus
├── docker/
│   ├── mariadb/my.cnf                    ← Config MariaDB binlog
│   ├── mariadb/init.sql                  ← Init: crear usuario replicador
│   └── prometheus/prometheus.yml         ← Config Prometheus
├── Dockerfile                            ← Multi-stage build optimizado
├── docker-compose.yml                    ← Stack completo de desarrollo
├── go.mod
└── .env.example
```

## Consideraciones de producción

### Escala horizontal
El servicio es **stateless excepto por la posición del binlog**. Para múltiples instancias:
1. Cambiar `POSITION_STORAGE=redis` e implementar `RedisPositionStore`
2. Cada instancia DEBE tener un `DB_SERVER_ID` único
3. Usar un load balancer (nginx/HAProxy) para distribuir conexiones WebSocket/SSE

### Riesgos del binlog
- Si el binlog rota antes de que el servicio lo consuma (falla prolongada), se pierden eventos históricos
- Ajustar `expire_logs_days` según el RTO de recuperación aceptable
- Monitorear `binlog_lag_seconds` en Grafana

### Seguridad
- El usuario `replicator` solo necesita `REPLICATION SLAVE` — nunca usar root
- Cifrar la conexión al binlog con TLS en producción (configurar `TLSConfig` en `BinlogSyncerConfig`)
- Ajustar `CheckOrigin` en el upgrader WebSocket para validar origen

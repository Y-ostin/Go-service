// dashboard_stream.go — Canal SSE único de actualizaciones en tiempo real del dashboard.
//
// Arquitectura del flujo:
//
//	MariaDB (INSERT/UPDATE/DELETE en tabla relevante)
//	  → BinlogListener → EventDispatcher → DashboardStreamBroker.Publish()
//	  → changeSignal (chan) → Run() bucle debounce 500ms
//	  → flushPending() → re-query SOLO métricas afectadas en BD
//	  → push eventos SSE tipados a los clientes que miran ese (año, mes)
//
// El cliente React solo necesita UNA conexión por tab:
//
//	const es = new EventSource(`/dashboard/stream?anio=2026&mes=2`)
//	es.addEventListener('ingresos',           e => setIngresos(JSON.parse(e.data)))
//	es.addEventListener('gastos',             e => setGastos(JSON.parse(e.data)))
//	es.addEventListener('resultado',          e => setResultado(JSON.parse(e.data)))
//	es.addEventListener('margen',             e => setMargen(JSON.parse(e.data)))
//	es.addEventListener('ingresos_cost_line', e => setPieLeft(JSON.parse(e.data)))
//	es.addEventListener('composicion_gastos', e => setPieRight(JSON.parse(e.data)))
//	es.addEventListener('punto_equilibrio',   e => setEquilibrio(JSON.parse(e.data)))
//	es.addEventListener('alerta_ejecutiva',   e => setAlerta(JSON.parse(e.data)))
//	es.addEventListener('top_riesgos',        e => setRiesgos(JSON.parse(e.data)))
//	es.addEventListener('ratio',              e => setRatio(JSON.parse(e.data)))
package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/application"
	"github.com/ccip/go-service/internal/domain"
	"github.com/ccip/go-service/pkg/metrics"
)

// tableMetrics mapea cada tabla relevante con las métricas del dashboard que afecta.
// Si una tabla no aparece aquí, sus eventos se descartan silenciosamente.
var tableMetrics = map[string][]string{
	"cicsa_charge_areas": {
		"ingresos", "resultado", "margen",
		"ingresos_cost_line", "punto_equilibrio",
		"alerta_ejecutiva", "top_riesgos", "ratio",
	},
	"general_expenses": {
		"gastos", "resultado", "margen",
		"composicion_gastos", "punto_equilibrio",
		"top_riesgos", "ratio",
	},
	"payment_approvals": {
		"gastos", "resultado", "margen", "punto_equilibrio", "ratio",
	},
	// Tablas de catálogo sin columna fecha propia — se notifica a TODOS los clientes activos
	"projects":   {"ingresos_cost_line", "alerta_ejecutiva"},
	"cost_lines": {"ingresos_cost_line", "composicion_gastos", "alerta_ejecutiva", "top_riesgos"},
}

// tablesWithoutFecha: no se puede extraer año/mes → fan-out a todos los filtros conectados.
var tablesWithoutFecha = map[string]bool{
	"projects":   true,
	"cost_lines": true,
}

// filterKey identifica de forma única un par (año, mes) para agrupar clientes.
type filterKey struct {
	Year  int
	Month int
}

// changeSignal encapsula un cambio detectado en el binlog.
type changeSignal struct {
	key     filterKey // año/mes extraído de la fila modificada
	metrics []string  // nombres de métricas afectadas por este cambio
	allKeys bool      // true → fan-out a todos los filtros con clientes activos
}

// streamClient representa una conexión SSE activa en /dashboard/stream.
type streamClient struct {
	id     string
	filter filterKey
	send   chan []byte
	closed atomic.Bool
}

// DashboardStreamBroker implementa domain.EventPublisher y http.Handler.
//
//   - Publish(): recibe binlog events del dispatcher, filtra tablas relevantes, encola señal.
//   - Run(): bucle de debounce que re-consulta la BD y empuja eventos SSE a los clientes.
//   - ServeHTTP(): abre la conexión SSE persistente para un client React.
type DashboardStreamBroker struct {
	svc     *application.DashboardService
	log     *zap.Logger
	m       *metrics.Metrics
	max     int
	counter atomic.Uint64 // IDs de cliente
	evtCtr  atomic.Uint64 // IDs de evento SSE (para Last-Event-ID)

	mu      sync.RWMutex
	clients map[string]*streamClient

	// signalCh recibe señales de cambio desde Publish() (llamado por el dispatcher)
	signalCh chan changeSignal
}

// NewDashboardStreamBroker construye el broker. Llamar Run(ctx) en goroutine dedicada.
func NewDashboardStreamBroker(
	svc *application.DashboardService,
	m *metrics.Metrics,
	log *zap.Logger,
	maxClients int,
) *DashboardStreamBroker {
	return &DashboardStreamBroker{
		svc:      svc,
		log:      log,
		m:        m,
		max:      maxClients,
		clients:  make(map[string]*streamClient),
		signalCh: make(chan changeSignal, 1024),
	}
}

// ── domain.EventPublisher ─────────────────────────────────────────────────────

// Publish es llamado por el EventDispatcher para cada BinlogEvent procesado.
// Opera de forma no-bloqueante: descarta si el canal de señales está lleno.
func (b *DashboardStreamBroker) Publish(event *domain.BinlogEvent) {
	affected, ok := tableMetrics[event.Table]
	if !ok {
		return // tabla no relevante para el dashboard
	}

	allKeys := tablesWithoutFecha[event.Table]
	var key filterKey
	if !allKeys {
		key = extractFilterKey(event)
	}

	sig := changeSignal{key: key, metrics: affected, allKeys: allKeys}
	select {
	case b.signalCh <- sig:
	default:
		b.log.Warn("dashboard stream: señalCh lleno, descartando cambio",
			zap.String("table", event.Table))
	}
}

// ClientCount implementa domain.EventPublisher.
func (b *DashboardStreamBroker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// ── Bucle de procesamiento ────────────────────────────────────────────────────

// Run inicia el bucle de debounce de 500ms. Debe ejecutarse en goroutine dedicada.
// Bloquea hasta que ctx sea cancelado.
func (b *DashboardStreamBroker) Run(ctx context.Context) {
	// pending acumula por filterKey el conjunto de métricas a refrescar.
	// La ventana de 500ms coalesce ráfagas de cambios antes de consultar la BD.
	pending := make(map[filterKey]map[string]struct{})
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	b.log.Info("dashboard stream broker iniciado")

	for {
		select {
		case <-ctx.Done():
			return

		case sig := <-b.signalCh:
			if sig.allKeys {
				// Tabla sin fecha: aplicar a todos los filtros con clientes activos
				b.mu.RLock()
				for _, c := range b.clients {
					addMetricsToKey(pending, c.filter, sig.metrics)
				}
				b.mu.RUnlock()
			} else {
				addMetricsToKey(pending, sig.key, sig.metrics)
			}

		case <-ticker.C:
			if len(pending) == 0 {
				continue
			}
			// Snapshot atómico + reset: el bucle continúa recibiendo señales
			// mientras flushPending trabaja en la goroutine separada.
			snapshot := pending
			pending = make(map[filterKey]map[string]struct{})
			go b.flushPending(ctx, snapshot)
		}
	}
}

// addMetricsToKey agrega nombres de métricas al conjunto pendiente de un filterKey.
func addMetricsToKey(pending map[filterKey]map[string]struct{}, k filterKey, names []string) {
	if pending[k] == nil {
		pending[k] = make(map[string]struct{})
	}
	for _, n := range names {
		pending[k][n] = struct{}{}
	}
}

// flushPending consulta la BD para cada (año,mes) pendiente y empuja mensajes SSE.
// Clave de eficiencia: si nadie mira un (año,mes), se salta la consulta BD por completo.
func (b *DashboardStreamBroker) flushPending(ctx context.Context, snapshot map[filterKey]map[string]struct{}) {
	for key, metricSet := range snapshot {
		// Encontrar clientes activos para este filtro
		b.mu.RLock()
		var targets []*streamClient
		for _, c := range b.clients {
			if !c.closed.Load() && c.filter == key {
				targets = append(targets, c)
			}
		}
		b.mu.RUnlock()

		if len(targets) == 0 {
			continue // nadie mira ese mes — skip BD completamente
		}

		msgs := b.buildSSEMessages(ctx, domain.DashboardFilter{Year: key.Year, Month: key.Month}, metricSet)
		if len(msgs) == 0 {
			continue
		}

		// Distribuir a cada cliente del filtro de forma no-bloqueante
		for _, c := range targets {
			if c.closed.Load() {
				continue
			}
			for _, msg := range msgs {
				select {
				case c.send <- msg:
				default:
					b.m.BinlogEventsDropped.Inc()
				}
			}
		}

		b.log.Debug("stream: métricas enviadas",
			zap.Int("anio", key.Year),
			zap.Int("mes", key.Month),
			zap.Int("metrics", len(metricSet)),
			zap.Int("clients", len(targets)),
		)
	}
}

// buildSSEMessages ejecuta las consultas SQL necesarias y retorna mensajes SSE formateados.
// Formato: "event: <nombre>\nid: ds-<n>\ndata: <json>\n\n"
// Timeout de 10s por lote de consultas.
func (b *DashboardStreamBroker) buildSSEMessages(
	ctx context.Context,
	f domain.DashboardFilter,
	metricSet map[string]struct{},
) [][]byte {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var msgs [][]byte

	push := func(eventName string, data interface{}) {
		raw, err := json.Marshal(data)
		if err != nil {
			b.log.Error("stream: error serializando métrica",
				zap.String("metric", eventName), zap.Error(err))
			return
		}
		id := b.evtCtr.Add(1)
		msgs = append(msgs, []byte(fmt.Sprintf("event: %s\nid: ds-%d\ndata: %s\n\n", eventName, id, raw)))
	}

	for metric := range metricSet {
		switch metric {
		case "ingresos":
			if res, err := b.svc.GetIngresos(qctx, f); err == nil {
				push("ingresos", res)
			} else {
				b.log.Warn("stream: GetIngresos", zap.Error(err))
			}
		case "gastos":
			if res, err := b.svc.GetGastos(qctx, f); err == nil {
				push("gastos", res)
			} else {
				b.log.Warn("stream: GetGastos", zap.Error(err))
			}
		case "resultado":
			if res, err := b.svc.GetResultado(qctx, f); err == nil {
				push("resultado", res)
			} else {
				b.log.Warn("stream: GetResultado", zap.Error(err))
			}
		case "margen":
			if res, err := b.svc.GetMargen(qctx, f); err == nil {
				push("margen", res)
			} else {
				b.log.Warn("stream: GetMargen", zap.Error(err))
			}
		case "ingresos_cost_line":
			if res, err := b.svc.GetIngresosPorCostLine(qctx, f); err == nil {
				if res == nil {
					res = []domain.CostLineItem{}
				}
				push("ingresos_cost_line", res)
			} else {
				b.log.Warn("stream: GetIngresosPorCostLine", zap.Error(err))
			}
		case "composicion_gastos":
			if res, err := b.svc.GetComposicionGastos(qctx, f); err == nil {
				if res == nil {
					res = []domain.ComposicionItem{}
				}
				push("composicion_gastos", res)
			} else {
				b.log.Warn("stream: GetComposicionGastos", zap.Error(err))
			}
		case "punto_equilibrio":
			if res, err := b.svc.GetPuntoEquilibrio(qctx, f); err == nil {
				push("punto_equilibrio", res)
			} else {
				b.log.Warn("stream: GetPuntoEquilibrio", zap.Error(err))
			}
		case "alerta_ejecutiva":
			if res, err := b.svc.GetAlertaEjecutiva(qctx, f); err == nil {
				push("alerta_ejecutiva", res)
			} else {
				b.log.Warn("stream: GetAlertaEjecutiva", zap.Error(err))
			}
		case "top_riesgos":
			if res, err := b.svc.GetTopRiesgos(qctx, f); err == nil {
				if res == nil {
					res = []domain.RiesgoItem{}
				}
				push("top_riesgos", res)
			} else {
				b.log.Warn("stream: GetTopRiesgos", zap.Error(err))
			}
		case "ratio":
			if res, err := b.svc.GetRatio(qctx, f); err == nil {
				push("ratio", res)
			} else {
				b.log.Warn("stream: GetRatio", zap.Error(err))
			}
		}
	}

	return msgs
}

// ── http.Handler ──────────────────────────────────────────────────────────────

// ServeHTTP maneja GET /dashboard/stream?anio=AAAA&mes=M
//
// Abre la conexión SSE persistente. Por defecto usa el mes calendario actual
// si no se proveen parámetros. El browser reconecta automáticamente (retry: 3000).
func (b *DashboardStreamBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado por este servidor", http.StatusInternalServerError)
		return
	}

	b.mu.RLock()
	current := len(b.clients)
	b.mu.RUnlock()
	if current >= b.max {
		http.Error(w, "demasiados clientes de stream conectados", http.StatusServiceUnavailable)
		return
	}

	// Resolver filtro — default al mes calendario actual
	now := time.Now()
	key := filterKey{Year: now.Year(), Month: int(now.Month())}
	q := r.URL.Query()
	if v, err := strconv.Atoi(q.Get("anio")); err == nil && v >= 2000 && v <= 2100 {
		key.Year = v
	}
	if v, err := strconv.Atoi(q.Get("mes")); err == nil && v >= 1 && v <= 12 {
		key.Month = v
	}

	// Headers SSE obligatorios
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Primer frame: instrucción de retry + confirmación de suscripción
	fmt.Fprintf(w, "retry: 3000\n")
	fmt.Fprintf(w, ": stream listo anio=%d mes=%d\n\n", key.Year, key.Month)
	flusher.Flush()

	// Registrar cliente
	clientID := fmt.Sprintf("ds-%d", b.counter.Add(1))
	c := &streamClient{
		id:     clientID,
		filter: key,
		send:   make(chan []byte, 256),
	}
	b.mu.Lock()
	b.clients[clientID] = c
	b.mu.Unlock()
	b.m.ConnectedClients.Add(1)

	b.log.Info("dashboard stream: cliente conectado",
		zap.String("id", clientID),
		zap.String("remote", r.RemoteAddr),
		zap.Int("anio", key.Year),
		zap.Int("mes", key.Month),
		zap.Int("total_stream", b.ClientCount()),
	)

	defer func() {
		c.closed.Store(true)
		b.mu.Lock()
		delete(b.clients, clientID)
		b.mu.Unlock()
		b.m.ConnectedClients.Sub(1)
		b.log.Info("dashboard stream: cliente desconectado",
			zap.String("id", clientID),
			zap.Int("total_stream", b.ClientCount()),
		)
	}()

	// Heartbeat cada 15s para mantener viva la conexión a través de proxies / load balancers
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// Comentario SSE — no dispara event handlers en el cliente
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ── Helpers de extracción de fecha ────────────────────────────────────────────

// extractFilterKey determina el año/mes del evento inspeccionando el campo "fecha"
// en After (INSERT/UPDATE) o Before (DELETE). Si no se puede extraer, usa el mes actual.
func extractFilterKey(event *domain.BinlogEvent) filterKey {
	now := time.Now()
	fallback := filterKey{Year: now.Year(), Month: int(now.Month())}
	for _, row := range []domain.RowData{event.After, event.Before} {
		if row == nil {
			continue
		}
		val, ok := row["fecha"]
		if !ok {
			continue
		}
		if t, ok := parseRowDate(val); ok {
			return filterKey{Year: t.Year(), Month: int(t.Month())}
		}
	}
	return fallback
}

// parseRowDate convierte un valor crudo de RowData en time.Time.
// go-mysql-org/go-mysql puede entregar DATE/DATETIME como time.Time, string o []byte.
func parseRowDate(v interface{}) (time.Time, bool) {
	switch s := v.(type) {
	case time.Time:
		return s, true
	case string:
		return parseDateString(s)
	case []byte:
		return parseDateString(string(s))
	}
	return time.Time{}, false
}

// parseDateString parsea los formatos de fecha típicos de MariaDB.
func parseDateString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

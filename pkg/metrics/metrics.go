// Package metrics expone métricas en formato Prometheus para monitorear
// el comportamiento del servicio en producción.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics agrupa todos los contadores y gauges del servicio.
type Metrics struct {
	// BinlogEventsTotal cuenta el total de eventos procesados por tipo y tabla.
	BinlogEventsTotal *prometheus.CounterVec

	// BinlogEventsDropped cuenta eventos descartados por el throttler.
	BinlogEventsDropped prometheus.Counter

	// BinlogReconnections cuenta cuántas veces se reconectó al binlog.
	BinlogReconnections prometheus.Counter

	// BinlogLagSeconds mide el retraso entre el timestamp del evento y el tiempo actual.
	BinlogLagSeconds prometheus.Gauge

	// ConnectedClients indica cuántos clientes WebSocket/SSE están conectados.
	ConnectedClients prometheus.Gauge

	// EventBroadcastDuration mide el tiempo que toma emitir un evento a todos los clientes.
	EventBroadcastDuration prometheus.Histogram

	// WorkerQueueDepth indica cuántos eventos están esperando en la cola del worker pool.
	WorkerQueueDepth prometheus.Gauge

	// BatchesSent cuenta el total de batches enviados al broadcast.
	BatchesSent prometheus.Counter
}

// New registra y devuelve todas las métricas del servicio.
// Usa promauto para registro automático en el DefaultRegisterer.
func New() *Metrics {
	return &Metrics{
		BinlogEventsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "binlog_events_total",
				Help: "Total de eventos de binlog procesados por tipo y tabla.",
			},
			[]string{"event_type", "schema", "table"},
		),

		BinlogEventsDropped: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "binlog_events_dropped_total",
				Help: "Total de eventos descartados por el throttler (cola llena).",
			},
		),

		BinlogReconnections: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "binlog_reconnections_total",
				Help: "Total de reconexiones al stream del binlog de MariaDB.",
			},
		),

		BinlogLagSeconds: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "binlog_lag_seconds",
				Help: "Retraso en segundos entre el evento en MariaDB y su procesamiento en Go.",
			},
		),

		ConnectedClients: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "ws_connected_clients",
				Help: "Número actual de clientes WebSocket/SSE conectados.",
			},
		),

		EventBroadcastDuration: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "broadcast_duration_seconds",
				Help:    "Tiempo que tarda en enviarse un evento a todos los clientes.",
				Buckets: prometheus.DefBuckets,
			},
		),

		WorkerQueueDepth: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "worker_queue_depth",
				Help: "Eventos pendientes en la cola del worker pool.",
			},
		),

		BatchesSent: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "batches_sent_total",
				Help: "Total de batches de eventos enviados al broadcast.",
			},
		),
	}
}

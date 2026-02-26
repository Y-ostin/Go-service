// Package application contiene los casos de uso del sistema.
// Orquesta la capa de dominio y la infraestructura sin depender de frameworks.
package application

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/config"
	"github.com/ccip/go-service/internal/domain"
	"github.com/ccip/go-service/pkg/metrics"
)

// EventDispatcher es el caso de uso central. Recibe eventos del binlog
// y los distribuye a múltiples publishers (WebSocket + SSE) aplicando:
//
//   - Worker pool para procesamiento concurrente
//   - Throttling para evitar inundar el frontend
//   - Batching: agrupar eventos en ventanas de tiempo
//   - Deduplicación por ID de evento (idempotencia)
//
// Arquitectura de flujo:
//
//	BinlogListener → [inputCh] → WorkerPool → [batcherCh] → Batcher → Publishers
type EventDispatcher struct {
	cfg        *config.ThrottleConfig
	publishers []domain.EventPublisher
	metrics    *metrics.Metrics
	log        *zap.Logger

	// inputCh recibe eventos crudos del binlog listener
	inputCh chan *domain.BinlogEvent

	// seenIDs es el cache de IDs procesados recientemente para idempotencia.
	// Se limpia cada cleanupInterval para evitar crecimiento ilimitado.
	seenMu   sync.Mutex
	seenIDs  map[string]struct{}

	// batchCh recibe eventos ya procesados listos para broadcast
	batchCh chan *domain.BinlogEvent
}

// NewEventDispatcher construye el dispatcher con los publishers configurados.
// publishers puede contener el WebSocket Hub, el SSE Broker, o ambos.
func NewEventDispatcher(
	cfg *config.ThrottleConfig,
	publishers []domain.EventPublisher,
	m *metrics.Metrics,
	log *zap.Logger,
) *EventDispatcher {
	return &EventDispatcher{
		cfg:        cfg,
		publishers:  publishers,
		metrics:    m,
		log:        log,
		inputCh:    make(chan *domain.BinlogEvent, cfg.WorkerPoolSize*100),
		seenIDs:    make(map[string]struct{}, 1024),
		batchCh:    make(chan *domain.BinlogEvent, cfg.BatchMaxSize*10),
	}
}

// Publish implementa domain.EventPublisher.
// Es llamado por el BinlogListener para cada evento detectado.
// No bloquea: si la cola está llena, el evento se descarta con métrica.
func (d *EventDispatcher) Publish(event *domain.BinlogEvent) {
	select {
	case d.inputCh <- event:
		d.metrics.WorkerQueueDepth.Set(float64(len(d.inputCh)))
	default:
		// Cola llena: sistema bajo presión extrema
		d.metrics.BinlogEventsDropped.Inc()
		d.log.Warn("cola del dispatcher llena, descartando evento",
			zap.String("table", event.Table),
			zap.String("type", string(event.Type)),
		)
	}
}

// ClientCount devuelve el total de clientes sumando todos los publishers.
func (d *EventDispatcher) ClientCount() int {
	total := 0
	for _, p := range d.publishers {
		total += p.ClientCount()
	}
	return total
}

// Run inicia el worker pool y el batcher. Bloquea hasta que ctx sea cancelado.
// Debe ejecutarse en una goroutine dedicada desde main.
func (d *EventDispatcher) Run(ctx context.Context) {
	// Iniciar N workers
	var wg sync.WaitGroup
	for i := 0; i < d.cfg.WorkerPoolSize; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			d.worker(ctx, workerID)
		}(i)
	}

	// Iniciar el batcher que agrupa eventos y los emite en ventanas de tiempo
	go d.batcher(ctx)

	// Limpiar seenIDs cada 5 minutos para evitar memory leak
	go d.cleanupSeenIDs(ctx)

	wg.Wait()
	d.log.Info("todos los workers del dispatcher terminaron")
}

// worker procesa eventos del inputCh: aplica idempotencia y los pasa al batchCh.
func (d *EventDispatcher) worker(ctx context.Context, id int) {
	d.log.Debug("worker del dispatcher iniciado", zap.Int("worker_id", id))

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-d.inputCh:
			if !ok {
				return
			}

			// Idempotencia: verificar si ya procesamos este ID
			if d.isDuplicate(evt.ID) {
				d.log.Debug("evento duplicado descartado", zap.String("id", evt.ID))
				continue
			}

			d.markSeen(evt.ID)
			d.metrics.WorkerQueueDepth.Set(float64(len(d.inputCh)))

			// Enviar al batcher
			select {
			case d.batchCh <- evt:
			case <-ctx.Done():
				return
			}
		}
	}
}

// batcher agrupa eventos en ventanas de tiempo y los emite como batch.
// Esto reduce la presión sobre los publishers cuando hay ráfagas de eventos.
//
// Estrategia:
//   - BatchWindow (ej: 50ms): tiempo máximo de acumulación
//   - BatchMaxSize (ej: 50): max eventos por batch
//
// El batch se emite al cumplirse cualquiera de las dos condiciones.
func (d *EventDispatcher) batcher(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.BatchWindow)
	defer ticker.Stop()

	batch := make([]*domain.BinlogEvent, 0, d.cfg.BatchMaxSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		d.broadcast(batch)
		d.metrics.BatchesSent.Inc()
		batch = batch[:0] // reset sin realocar
	}

	for {
		select {
		case <-ctx.Done():
			flush() // enviar lo que queda antes de salir
			return

		case evt, ok := <-d.batchCh:
			if !ok {
				return
			}
			batch = append(batch, evt)

			// Flush anticipado si el batch alcanzó el máximo
			if len(batch) >= d.cfg.BatchMaxSize {
				flush()
			}

		case <-ticker.C:
			// Flush por tiempo aunque el batch no esté lleno
			flush()
		}
	}
}

// broadcast emite un batch de eventos a todos los publishers registrados.
// Cada evento se emite individualmente para mantener la granularidad.
func (d *EventDispatcher) broadcast(batch []*domain.BinlogEvent) {
	for _, evt := range batch {
		for _, pub := range d.publishers {
			pub.Publish(evt)
		}
	}
	d.log.Debug("batch emitido",
		zap.Int("size", len(batch)),
		zap.Int("ws_clients", d.ClientCount()),
	)
}

// isDuplicate verifica si el ID ya fue procesado (thread-safe).
func (d *EventDispatcher) isDuplicate(id string) bool {
	d.seenMu.Lock()
	defer d.seenMu.Unlock()
	_, ok := d.seenIDs[id]
	return ok
}

// markSeen registra el ID como procesado (thread-safe).
func (d *EventDispatcher) markSeen(id string) {
	d.seenMu.Lock()
	defer d.seenMu.Unlock()
	d.seenIDs[id] = struct{}{}
}

// cleanupSeenIDs limpia el mapa de IDs vistos cada 5 minutos.
// Los IDs del binlog son únicos por ejecución y solo necesitamos
// protección contra duplicados a muy corto plazo.
func (d *EventDispatcher) cleanupSeenIDs(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.seenMu.Lock()
			oldSize := len(d.seenIDs)
			d.seenIDs = make(map[string]struct{}, 1024)
			d.seenMu.Unlock()
			d.log.Debug("seenIDs limpiado", zap.Int("prev_size", oldSize))
		}
	}
}

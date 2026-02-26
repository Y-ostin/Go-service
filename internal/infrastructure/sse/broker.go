// Package sse implementa el broker de Server-Sent Events (SSE) para push
// unidireccional del servidor al dashboard. SSE es nativamente soportado
// por todos los browsers modernos con reconexión automática incorporada.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/domain"
	"github.com/ccip/go-service/pkg/metrics"
)

// sseClient representa una conexión SSE activa de un cliente HTTP.
type sseClient struct {
	id     string
	send   chan []byte
	closed atomic.Bool
}

// Broker gestiona todas las conexiones SSE y el broadcast de eventos.
// SSE vs WebSocket: SSE es unidireccional (server → client), más simple,
// con reconexión automática nativa del browser mediante el campo 'retry'.
type Broker struct {
	mu         sync.RWMutex
	clients    map[string]*sseClient
	maxClients int
	log        *zap.Logger
	metrics    *metrics.Metrics
	counter    atomic.Uint64
}

// NewBroker construye el broker SSE.
func NewBroker(maxClients int, m *metrics.Metrics, log *zap.Logger) *Broker {
	return &Broker{
		clients:    make(map[string]*sseClient),
		maxClients: maxClients,
		log:        log,
		metrics:    m,
	}
}

// Publish envía un evento a todos los clientes SSE conectados.
// No bloquea: si el buffer de un cliente está lleno, se descarta el mensaje.
func (b *Broker) Publish(event *domain.BinlogEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		b.log.Error("error serializando evento para SSE", zap.Error(err))
		return
	}

	// Formato SSE: "data: <json>\n\n"
	// Añadimos el ID del evento para que el browser pueda resumir con Last-Event-ID
	sseMsg := fmt.Sprintf("id: %s\ndata: %s\n\n", event.ID, data)
	msgBytes := []byte(sseMsg)

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, c := range b.clients {
		if c.closed.Load() {
			continue
		}
		select {
		case c.send <- msgBytes:
		default:
			b.metrics.BinlogEventsDropped.Inc()
		}
	}
}

// ClientCount devuelve el número actual de clientes SSE conectados.
func (b *Broker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// ServeHTTP es el handler HTTP para el endpoint SSE (/events).
// El cliente solo necesita: new EventSource('/events')
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Verificar si el ResponseWriter soporta streaming (Flusher)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado por este servidor", http.StatusInternalServerError)
		return
	}

	// Verificar límite
	b.mu.RLock()
	current := len(b.clients)
	b.mu.RUnlock()

	if current >= b.maxClients {
		http.Error(w, "demasiados clientes conectados", http.StatusServiceUnavailable)
		return
	}

	// Headers SSE obligatorios
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// CORS: ajustar al dominio del frontend en producción
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Retry: el browser intentará reconectar cada 3 segundos si se pierde la conexión
	fmt.Fprintf(w, "retry: 3000\n\n")
	flusher.Flush()

	// Registrar cliente
	clientID := fmt.Sprintf("sse-%d", b.counter.Add(1))
	c := &sseClient{
		id:   clientID,
		send: make(chan []byte, 256),
	}

	b.mu.Lock()
	b.clients[clientID] = c
	b.mu.Unlock()
	b.metrics.ConnectedClients.Add(1)

	b.log.Debug("cliente SSE conectado",
		zap.String("id", clientID),
		zap.String("remote", r.RemoteAddr),
		zap.Int("total", b.ClientCount()),
	)

	// Limpiar al desconectar
	defer func() {
		c.closed.Store(true)
		b.mu.Lock()
		delete(b.clients, clientID)
		b.mu.Unlock()
		b.metrics.ConnectedClients.Sub(1)
		b.log.Debug("cliente SSE desconectado",
			zap.String("id", clientID),
			zap.Int("total", b.ClientCount()),
		)
	}()

	// Heartbeat cada 15 segundos para mantener la conexión abierta
	// y evitar que proxies/load balancers la cierren por inactividad
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Loop principal: enviar mensajes o esperar desconexión
	for {
		select {
		case <-r.Context().Done():
			// Cliente cerró la conexión
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
			// Un comentario SSE (línea con ':') sirve como keepalive
			// y no activa el event handler en el cliente
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

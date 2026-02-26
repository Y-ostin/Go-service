// Package websocket implementa el hub de WebSocket para broadcast de eventos
// del binlog a múltiples clientes concurrentes.
package websocket

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/domain"
	"github.com/ccip/go-service/pkg/metrics"
)

// upgrader configura el upgrade HTTP → WebSocket.
// CheckOrigin permisivo para desarrollo; en prod validar el origen.
var upgrader = gorillaws.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// TODO producción: validar r.Header.Get("Origin") contra whitelist
		return true
	},
	HandshakeTimeout: 10 * time.Second,
}

// client representa una conexión WebSocket activa.
type client struct {
	conn   *gorillaws.Conn
	send   chan []byte // buffer de mensajes salientes
	closed atomic.Bool
}

// Hub gestiona todas las conexiones WebSocket activas y el broadcast de mensajes.
// Es thread-safe: múltiples goroutines pueden registrar/eliminar clients y
// publicar mensajes de forma concurrente.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	maxClients int
	log        *zap.Logger
	metrics    *metrics.Metrics

	// Canales de administración del hub
	register   chan *client
	unregister chan *client
}

// NewHub construye el Hub con el límite de clientes configurado.
func NewHub(maxClients int, m *metrics.Metrics, log *zap.Logger) *Hub {
	return &Hub{
		clients:    make(map[*client]struct{}),
		maxClients: maxClients,
		log:        log,
		metrics:    m,
		register:   make(chan *client, 16),
		unregister: make(chan *client, 16),
	}
}

// Run inicia el loop de gestión del hub. Debe ejecutarse en su propia goroutine.
// Bloquea hasta que ctx sea cancelado (en main.go se cancela en shutdown).
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			h.metrics.ConnectedClients.Set(float64(len(h.clients)))
			h.log.Debug("cliente WebSocket conectado",
				zap.Int("total_clients", len(h.clients)),
			)

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
			h.metrics.ConnectedClients.Set(float64(len(h.clients)))
			h.log.Debug("cliente WebSocket desconectado",
				zap.Int("total_clients", len(h.clients)),
			)
		}
	}
}

// Publish envía un evento a TODOS los clientes conectados de forma no-bloqueante.
// Si el buffer de un cliente está lleno, se descarta el mensaje para ese cliente
// (nunca bloquea el broadcaster).
func (h *Hub) Publish(event *domain.BinlogEvent) {
	start := time.Now()

	data, err := json.Marshal(event)
	if err != nil {
		h.log.Error("error serializando evento para WebSocket", zap.Error(err))
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.closed.Load() {
			continue
		}
		select {
		case c.send <- data:
		default:
			// Buffer lleno: el cliente está lento — descartamos este mensaje
			// en lugar de bloquear el broadcast global
			h.metrics.BinlogEventsDropped.Inc()
			h.log.Warn("buffer de cliente WebSocket lleno, descartando evento",
				zap.String("remote_addr", c.conn.RemoteAddr().String()),
			)
		}
	}

	h.metrics.EventBroadcastDuration.Observe(time.Since(start).Seconds())
}

// ClientCount devuelve el número actual de clientes conectados.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ServeHTTP es el handler HTTP para el endpoint WebSocket (/ws).
// Se puede registrar directamente en el router: mux.Handle("/ws", hub)
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Verificar límite de clientes
	h.mu.RLock()
	currentClients := len(h.clients)
	h.mu.RUnlock()

	if currentClients >= h.maxClients {
		h.log.Warn("límite de clientes WebSocket alcanzado, rechazando conexión",
			zap.Int("max", h.maxClients),
			zap.Int("current", currentClients),
		)
		http.Error(w, "demasiados clientes conectados", http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error("error upgrading WebSocket", zap.Error(err))
		return
	}

	c := &client{
		conn: conn,
		// Buffer de 256 mensajes por cliente — suficiente para picos cortos
		send: make(chan []byte, 256),
	}

	h.register <- c

	// Goroutine de escritura (server → cliente)
	go c.writePump(h)

	// Goroutine de lectura (cliente → servidor)
	// Necesaria para detectar pings/pongs y desconexiones del cliente
	go c.readPump(h)
}

// writePump envía mensajes del channel 'send' al WebSocket.
// Se ejecuta en su propia goroutine por cliente.
func (c *client) writePump(h *Hub) {
	const (
		writeWait  = 10 * time.Second
		pingPeriod = 30 * time.Second
	)

	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.closed.Store(true)
		h.unregister <- c
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub cerró el canal
				_ = c.conn.WriteMessage(gorillaws.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(gorillaws.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			// Ping para mantener la conexión viva
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(gorillaws.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump lee mensajes del WebSocket (principalmente para detectar desconexiones).
// En este sistema el cliente no envía datos, pero necesitamos el read loop
// para que gorilla/websocket gestione pings/pongs y detecte cierre de conexión.
func (c *client) readPump(h *Hub) {
	const (
		pongWait   = 60 * time.Second
		maxMsgSize = 512
	)

	defer func() {
		c.closed.Store(true)
		h.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMsgSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// Leer y descartar — solo nos importa detectar desconexión
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// Package http implementa el servidor HTTP del servicio con endpoints para:
//   - WebSocket (/ws)
//   - SSE (/events)
//   - Health check (/health)
//   - Métricas Prometheus (/metrics)
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/config"
	"github.com/ccip/go-service/internal/domain"
)

// Server encapsula el servidor HTTP con todos sus handlers.
type Server struct {
	httpServer *http.Server
	log        *zap.Logger
	cfg        *config.ServerConfig
	wsHandler  http.Handler
	sseHandler http.Handler
	dispatcher domain.EventPublisher
}

// NewServer construye el servidor HTTP con todos los handlers inyectados.
func NewServer(
	cfg *config.ServerConfig,
	wsHandler http.Handler,
	sseHandler http.Handler,
	dispatcher domain.EventPublisher,
	dashHandler *DashboardHandler,
	streamHandler http.Handler,
	log *zap.Logger,
) *Server {
	s := &Server{
		log:        log,
		cfg:        cfg,
		wsHandler:  wsHandler,
		sseHandler: sseHandler,
		dispatcher: dispatcher,
	}

	mux := http.NewServeMux()

	// ── CORS wrapper: permite acceso desde cualquier origen (dev local) ────
	withCORS := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Cache-Control")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.ServeHTTP(w, r)
		})
	}

	// ── Endpoints de streaming ─────────────────────────────────────────────
	mux.Handle("/ws", withCORS(loggingMiddleware(log, wsHandler)))
	mux.Handle("/events", withCORS(loggingMiddleware(log, sseHandler)))

	// ── Health check ──────────────────────────────────────────────────────
	mux.HandleFunc("/health", s.handleHealth)

	// ── Métricas Prometheus ───────────────────────────────────────────────
	mux.Handle("/metrics", promhttp.Handler())

	// ── Dashboard ejecutivo (HTML estático) ───────────────────────────────
	mux.HandleFunc("/dashboard", s.handleDashboard)
	mux.HandleFunc("/", s.handleRoot)

	// ── Dashboard info ────────────────────────────────────────────────────
	mux.HandleFunc("/info", s.handleInfo)

	// ── Dashboard financiero ──────────────────────────────────────────────
	// Todos los endpoints aceptan ?anio=AAAA&mes=M
	if dashHandler != nil {
		mux.Handle("/dashboard/ingresos", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleIngresos)))
		mux.Handle("/dashboard/gastos", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleGastos)))
		mux.Handle("/dashboard/resultado", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleResultado)))
		mux.Handle("/dashboard/margen", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleMargen)))
		mux.Handle("/dashboard/ingresos-cost-line", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleIngresosCostLine)))
		mux.Handle("/dashboard/composicion-gastos", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleComposicionGastos)))
		mux.Handle("/dashboard/punto-equilibrio", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandlePuntoEquilibrio)))
		mux.Handle("/dashboard/alerta-ejecutiva", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleAlertaEjecutiva)))
		mux.Handle("/dashboard/top-riesgos", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleTopRiesgos)))
		mux.Handle("/dashboard/ratio", loggingMiddleware(log, http.HandlerFunc(dashHandler.HandleRatio)))
	}

	// ── Dashboard stream (SSE canal único de cambios en tiempo real) ─────────────
	// GET /dashboard/stream?anio=AAAA&mes=M
	// Un evento SSE tipado por métrica afectada, sin polling, sin saturar el cliente.
	if streamHandler != nil {
		mux.Handle("/dashboard/stream", loggingMiddleware(log, streamHandler))
	}

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: 0, // 0 = sin timeout en escritura (necesario para SSE/WS)
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// corsMiddleware añade los headers CORS necesarios para que el frontend React pueda consumir la API.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Start inicia el servidor HTTP. Bloquea hasta error o cierre.
func (s *Server) Start() error {
	s.log.Info("servidor HTTP iniciado",
		zap.String("addr", s.httpServer.Addr),
		zap.String("ws_endpoint", fmt.Sprintf("ws://localhost%s/ws", s.httpServer.Addr)),
		zap.String("sse_endpoint", fmt.Sprintf("http://localhost%s/events", s.httpServer.Addr)),
		zap.String("metrics_endpoint", fmt.Sprintf("http://localhost%s/metrics", s.httpServer.Addr)),
	)
	return s.httpServer.ListenAndServe()
}

// Shutdown realiza un shutdown graceful con el contexto provisto.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handleHealth devuelve el estado del servicio en formato JSON.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"status":           "ok",
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
		"connected_clients": s.dispatcher.ClientCount(),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error("error escribiendo health response", zap.Error(err))
	}
}

// handleInfo devuelve información de configuración no sensible del servicio.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"service":          "go-binlog-service",
		"version":          "1.0.0",
		"connected_clients": s.dispatcher.ClientCount(),
		"endpoints": map[string]string{
			"websocket": "/ws",
			"sse":       "/events",
			"health":    "/health",
			"metrics":   "/metrics",
		},
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// handleDashboard sirve el archivo dashboard.html embebido en el binario.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, "/app/dashboard.html")
}

// handleRoot redirige la raíz al dashboard.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// loggingMiddleware registra cada request HTTP con método, path y duración.
func loggingMiddleware(log *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Info("request HTTP",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remote", r.RemoteAddr),
			zap.Duration("duration", time.Since(start)),
		)
	})
}

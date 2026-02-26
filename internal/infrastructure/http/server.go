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

	// ── Endpoints de streaming ─────────────────────────────────────────────
	mux.Handle("/ws", loggingMiddleware(log, wsHandler))
	mux.Handle("/events", loggingMiddleware(log, sseHandler))

	// ── Health check ──────────────────────────────────────────────────────
	mux.HandleFunc("/health", s.handleHealth)

	// ── Métricas Prometheus ───────────────────────────────────────────────
	mux.Handle("/metrics", promhttp.Handler())

	// ── Dashboard info ────────────────────────────────────────────────────
	mux.HandleFunc("/info", s.handleInfo)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: 0, // 0 = sin timeout en escritura (necesario para SSE/WS)
		IdleTimeout:  120 * time.Second,
	}

	return s
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

// Package http implementa el servidor HTTP del servicio con endpoints para:
//   - GET /dashboard           → dashboard HTML desde disco
//   - GET /health              → health check JSON
//   - GET /metrics             → métricas Prometheus
//   - GET /api/kpis            → KPIs financieros del mes
//   - GET /api/kpis/periods    → períodos con data disponible
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ccip/go-service/internal/config"
)

// Server encapsula el servidor HTTP con el endpoint de KPIs.
type Server struct {
httpServer *http.Server
log        *zap.Logger
cfg        *config.ServerConfig
gormDB     *gorm.DB
}

// NewServer construye el servidor HTTP con el handler de KPIs.
func NewServer(
cfg *config.ServerConfig,
dbCfg *config.DBConfig,
gormDB *gorm.DB,
log *zap.Logger,
) *Server {
s := &Server{
log:    log,
cfg:    cfg,
gormDB: gormDB,
}

mux := http.NewServeMux()

// CORS wrapper: permite acceso desde cualquier origen (dev local)
	withCORS := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Cache-Control")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.ServeHTTP(w, r)
		})
	}

// ── Endpoints ─────────────────────────────────────────────────────────────
	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/metrics", promhttp.Handler())

	// ── Dashboard HTML desde disco ──────────────────────────────────────────
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "dashboard.html")
	})

	// ── API KPIs financieros (GORM) ───────────────────────────────────────
	kpiHandler := NewKPIHandler(s.gormDB, log)
	mux.Handle("/api/kpis", withCORS(kpiHandler))
	mux.Handle("/api/kpis/periods",   withCORS(http.HandlerFunc(kpiHandler.ServePeriodsHTTP)))
	mux.Handle("/api/kpis/breakdown", withCORS(http.HandlerFunc(kpiHandler.ServeBreakdownHTTP)))

s.httpServer = &http.Server{
Addr:         fmt.Sprintf(":%d", cfg.Port),
Handler:      mux,
ReadTimeout:  cfg.ReadTimeout,
WriteTimeout: 30 * time.Second,
IdleTimeout:  120 * time.Second,
}

return s
}

// Start inicia el servidor HTTP. Bloquea hasta error o cierre.
func (s *Server) Start() error {
s.log.Info("servidor HTTP iniciado",
zap.String("addr",      s.httpServer.Addr),
zap.String("dashboard", fmt.Sprintf("http://localhost%s/dashboard", s.httpServer.Addr)),
zap.String("health",    fmt.Sprintf("http://localhost%s/health",    s.httpServer.Addr)),
zap.String("metrics",   fmt.Sprintf("http://localhost%s/metrics",   s.httpServer.Addr)),
zap.String("kpis",      fmt.Sprintf("http://localhost%s/api/kpis",  s.httpServer.Addr)),
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
"status":    "ok",
"timestamp": time.Now().UTC().Format(time.RFC3339),
"service":   "go-binlog-service",
}

if err := json.NewEncoder(w).Encode(resp); err != nil {
s.log.Error("error escribiendo health response", zap.Error(err))
}
}

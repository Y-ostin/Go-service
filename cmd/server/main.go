// cmd/server/main.go — Punto de entrada del servicio Go de binlog replication.
//
// Arquitectura hexagonal aplicada:
//   Domain     → domain/ (entidades, puertos)
//   Application → application/ (casos de uso: dispatcher, throttler)
//   Infrastructure → infrastructure/ (binlog, websocket, sse, http)
//
// Flujo de datos:
//   MariaDB binlog → Listener → Dispatcher (worker pool + batcher) → Hub WS + SSE Broker → Dashboard
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/application"
	"github.com/ccip/go-service/internal/config"
	"github.com/ccip/go-service/internal/domain"
	binloginfra "github.com/ccip/go-service/internal/infrastructure/binlog"
	httpinfra "github.com/ccip/go-service/internal/infrastructure/http"
	"github.com/ccip/go-service/internal/infrastructure/sse"
	wsinfra "github.com/ccip/go-service/internal/infrastructure/websocket"
	"github.com/ccip/go-service/pkg/logger"
	"github.com/ccip/go-service/pkg/metrics"
)

func main() {
	// ── 1. Cargar configuración ────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		// No podemos usar el logger aún, usamos stderr
		_, _ = os.Stderr.WriteString("error cargando configuración: " + err.Error() + "\n")
		os.Exit(1)
	}

	// ── 2. Inicializar logger estructurado ─────────────────────────────────
	log := logger.MustNew(cfg.Log.Level, cfg.Log.Format)
	defer func() { _ = log.Sync() }()

	log.Info("iniciando go-binlog-service",
		zap.String("db_host", cfg.DB.Host),
		zap.Uint16("db_port", cfg.DB.Port),
		zap.Int("server_port", cfg.Server.Port),
		zap.Strings("watched_tables", cfg.Binlog.WatchedTables),
		zap.Strings("watched_schemas", cfg.Binlog.WatchedSchemas),
	)

	// ── 3. Inicializar métricas Prometheus ─────────────────────────────────
	m := metrics.New()

	// ── 4. Inicializar Position Store ──────────────────────────────────────
	posStore, err := binloginfra.NewFilePositionStore(cfg.Position.FilePath, log)
	if err != nil {
		log.Fatal("error inicializando position store", zap.Error(err))
	}

	// ── 5. Inicializar Publishers (WebSocket Hub + SSE Broker) ─────────────
	wsHub := wsinfra.NewHub(cfg.Server.MaxClients, m, log)
	sseBroker := sse.NewBroker(cfg.Server.MaxClients, m, log)

	// ── 6. Inicializar Event Dispatcher (worker pool + batcher) ───────────
	// El dispatcher implementa domain.EventPublisher y recibe eventos del Listener.
	// Internamente los distribuye al wsHub y sseBroker.
	dispatcher := application.NewEventDispatcher(
		&cfg.Throttle,
		[]domain.EventPublisher{wsHub, sseBroker},
		m,
		log,
	)

	// ── 7. Inicializar Binlog Listener ─────────────────────────────────────
	binlogListener := binloginfra.NewListener(cfg, posStore, dispatcher, m, log)

	// ── 8. Inicializar servidor HTTP ───────────────────────────────────────
	httpServer := httpinfra.NewServer(
		&cfg.Server,
		wsHub,
		sseBroker,
		dispatcher,
		log,
	)

	// ── 9. Context con cancelación para shutdown graceful ──────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── 10. Iniciar goroutines del sistema ─────────────────────────────────

	// WebSocket Hub loop (gestiona register/unregister de clientes WS)
	go wsHub.Run()

	// Dispatcher (worker pool + batcher)
	go dispatcher.Run(ctx)

	// Binlog Listener con reconexión automática
	go func() {
		if err := binlogListener.Start(ctx); err != nil {
			log.Error("binlog listener cerró con error", zap.Error(err))
		}
	}()

	// Servidor HTTP (WebSocket + SSE + metrics + health)
	go func() {
		if err := httpServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("error en servidor HTTP", zap.Error(err))
		}
	}()

	log.Info("servicio iniciado correctamente",
		zap.String("ws", "ws://localhost:"+itoa(cfg.Server.Port)+"/ws"),
		zap.String("sse", "http://localhost:"+itoa(cfg.Server.Port)+"/events"),
		zap.String("health", "http://localhost:"+itoa(cfg.Server.Port)+"/health"),
		zap.String("metrics", "http://localhost:"+itoa(cfg.Server.Port)+"/metrics"),
	)

	// ── 11. Esperar señal de shutdown (SIGTERM / SIGINT) ───────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	log.Info("señal de shutdown recibida, apagando servicio…",
		zap.String("signal", sig.String()),
	)

	// Cancelar context para detener binlog listener y dispatcher
	cancel()

	// Shutdown graceful del servidor HTTP (30 segundos para que terminen requests activos)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("error durante shutdown del servidor HTTP", zap.Error(err))
	}

	log.Info("servicio apagado correctamente")
}

// itoa convierte un entero a string.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

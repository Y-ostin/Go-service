// cmd/server/main.go — Punto de entrada del servicio Go de binlog replication.
//
// Flujo de datos:
//   MariaDB binlog → Listener → Dispatcher → MQTT Publisher (EMQX Cloud) → Frontend Dashboard
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
	mqttinfra "github.com/ccip/go-service/internal/infrastructure/mqtt"
	"github.com/ccip/go-service/pkg/logger"
	"github.com/ccip/go-service/pkg/metrics"
)

func main() {
	// ── 1. Cargar configuración ────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		_, _ = os.Stderr.WriteString("error cargando configuración: " + err.Error() + "\n")
		os.Exit(1)
	}

	// ── 2. Logger estructurado ────────────────────────────────────────────
	log := logger.MustNew(cfg.Log.Level, cfg.Log.Format)
	defer func() { _ = log.Sync() }()

	// ── 3. GORM — conexión a DB para queries de KPIs ──────────────────────
	gormDB, err := config.ConnectDB(&cfg.DB)
	if err != nil {
		log.Fatal("falla crítica al conectar base de datos con GORM", zap.Error(err))
	}
	log.Info("GORM: conexión establecida",
		zap.String("host", cfg.DB.Host),
		zap.String("db", cfg.DB.QueryDBName),
	)

	// ── 4. Métricas Prometheus ─────────────────────────────────────────────
	m := metrics.New()

	// ── 5. Persistencia de posición del binlog ─────────────────────────────
	posStore, err := binloginfra.NewFilePositionStore(cfg.Position.FilePath, log)
	if err != nil {
		log.Fatal("error inicializando position store", zap.Error(err))
	}

	// ── 6. MQTT Publisher (EMQX Cloud) ────────────────────────────────────
	var publishers []domain.EventPublisher

	if cfg.MQTT.Enabled {
		mqttPub, mqttErr := mqttinfra.NewPublisher(&cfg.MQTT, cfg.Server.Port, log)
		if mqttErr != nil {
			log.Warn("MQTT publisher no disponible, el servicio continua sin MQTT",
				zap.Error(mqttErr))
		} else {
			publishers = append(publishers, mqttPub)
			defer mqttPub.Close()
			log.Info("MQTT publisher activo",
				zap.String("broker", cfg.MQTT.Broker),
				zap.String("topic", cfg.MQTT.Topic),
			)
		}
	}

	// ── 7. Dispatcher (worker pool + batcher) ─────────────────────────────
	dispatcher := application.NewEventDispatcher(
		&cfg.Throttle,
		publishers,
		m,
		log,
	)

	// ── 8. Binlog Listener ─────────────────────────────────────────────────
	binlogListener := binloginfra.NewListener(cfg, posStore, dispatcher, m, log)

	// ── 9. Servidor HTTP (health + metrics + /api/kpis) ────────────────────
	httpServer := httpinfra.NewServer(
		&cfg.Server,
		&cfg.DB,
		gormDB,
		log,
	)

	// ── 10. Context con cancelación para shutdown graceful ─────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── 11. Iniciar goroutines ─────────────────────────────────────────────
	go dispatcher.Run(ctx)

	go func() {
		if err := binlogListener.Start(ctx); err != nil {
			log.Error("binlog listener cerró con error", zap.Error(err))
		}
	}()

	go func() {
		if err := httpServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("error en servidor HTTP", zap.Error(err))
		}
	}()

	log.Info("servicio iniciado correctamente",
		zap.String("health",     "http://localhost:"+itoa(cfg.Server.Port)+"/health"),
		zap.String("metrics",    "http://localhost:"+itoa(cfg.Server.Port)+"/metrics"),
		zap.String("kpis",       "http://localhost:"+itoa(cfg.Server.Port)+"/api/kpis"),
		zap.String("mqtt_topic", cfg.MQTT.Topic),
	)

	// ── 12. Esperar señal de shutdown (SIGTERM / SIGINT) ───────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	log.Info("señal de shutdown recibida, apagando servicio…",
		zap.String("signal", sig.String()),
	)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("error durante shutdown del servidor HTTP", zap.Error(err))
	}

	log.Info("servicio apagado correctamente")
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

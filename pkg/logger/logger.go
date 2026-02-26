// Package logger provee un logger estructurado basado en zap.
// En producción emite JSON. En desarrollo emite formato legible.
package logger

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New construye un *zap.Logger según el nivel y formato especificados.
// level: "debug" | "info" | "warn" | "error"
// format: "json" | "console"
func New(level, format string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(strings.ToLower(level))); err != nil {
		return nil, fmt.Errorf("nivel de log inválido '%s': %w", level, err)
	}

	var cfg zap.Config
	if strings.ToLower(format) == "console" {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg = zap.NewProductionConfig()
	}

	cfg.Level = zap.NewAtomicLevelAt(zapLevel)

	// Campos adicionales estándar para todos los logs del servicio
	cfg.InitialFields = map[string]interface{}{
		"service": "go-binlog-service",
		"version": "1.0.0",
	}

	logger, err := cfg.Build(
		zap.AddCallerSkip(0),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	if err != nil {
		return nil, fmt.Errorf("error construyendo logger: %w", err)
	}

	return logger, nil
}

// MustNew es igual a New pero hace panic si falla. Útil en main().
func MustNew(level, format string) *zap.Logger {
	l, err := New(level, format)
	if err != nil {
		panic(err)
	}
	return l
}

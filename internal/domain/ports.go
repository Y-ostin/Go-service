// Package domain — puertos (interfaces) de la arquitectura hexagonal.
// Estos contratos son implementados por la capa de infraestructura.
package domain

import "context"

// PositionRepository define el contrato para persistir y recuperar
// la posición del binlog. Permite reanudar desde el último estado conocido
// ante reconexiones o reinicios del servicio.
type PositionRepository interface {
	// Save persiste la posición actual del binlog de forma atómica.
	Save(ctx context.Context, pos BinlogPosition) error

	// Load recupera la última posición persistida.
	// Devuelve una posición cero si no hay ninguna guardada.
	Load(ctx context.Context) (BinlogPosition, error)
}

// EventPublisher define el contrato para emitir eventos de dominio
// hacia los clientes conectados (WebSocket, SSE, etc.).
type EventPublisher interface {
	// Publish envía el evento a todos los suscriptores activos.
	Publish(event *BinlogEvent)

	// ClientCount devuelve el número de clientes conectados actualmente.
	ClientCount() int
}

// BinlogListener define el contrato del servicio que consume el binlog.
type BinlogListener interface {
	// Start inicia el consumo del binlog y bloquea hasta que el contexto sea cancelado.
	Start(ctx context.Context) error
}

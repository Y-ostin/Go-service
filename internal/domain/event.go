// Package domain define las entidades y contratos del core del sistema.
// No tiene dependencias externas — pura lógica de negocio.
package domain

import (
	"time"
)

// EventType representa el tipo de operación capturada desde el binlog.
type EventType string

const (
	EventInsert EventType = "INSERT"
	EventUpdate EventType = "UPDATE"
	EventDelete EventType = "DELETE"
)

// RowData es un mapa genérico que representa una fila de la base de datos.
// key = nombre de columna, value = valor crudo del binlog.
type RowData map[string]interface{}

// BinlogEvent es la estructura de dominio que encapsula un evento detectado
// en el Binary Log de MariaDB. Es agnóstica a la librería go-mysql.
type BinlogEvent struct {
	// ID único del evento para idempotencia (generado por el dispatcher).
	ID string `json:"id"`

	// Tipo de la operación de base de datos.
	Type EventType `json:"type"`

	// Schema (base de datos) donde ocurrió el evento.
	Schema string `json:"schema"`

	// Table es el nombre de la tabla afectada.
	Table string `json:"table"`

	// Before contiene el estado de la fila ANTES del cambio (solo UPDATE/DELETE).
	Before RowData `json:"before,omitempty"`

	// After contiene el estado de la fila DESPUÉS del cambio (INSERT/UPDATE).
	After RowData `json:"after,omitempty"`

	// BinlogFile es el archivo binlog donde se encontró el evento.
	BinlogFile string `json:"binlog_file"`

	// BinlogPos es la posición dentro del archivo binlog.
	BinlogPos uint32 `json:"binlog_pos"`

	// Timestamp es el momento real en que ocurrió la transacción en MariaDB.
	Timestamp time.Time `json:"timestamp"`

	// ReceivedAt es el momento en que el servicio Go procesó el evento.
	ReceivedAt time.Time `json:"received_at"`
}

// BinlogPosition almacena la posición del replication stream para persistir
// y reanudar desde donde se quedó ante una reconexión o reinicio.
type BinlogPosition struct {
	File     string `json:"file"`
	Position uint32 `json:"position"`
}

// IsZero indica si la posición no ha sido inicializada.
func (p BinlogPosition) IsZero() bool {
	return p.File == "" && p.Position == 0
}

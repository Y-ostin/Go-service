package domain

import "time"

// KPIPayload agrupa los KPIs financieros mensuales para el dashboard ejecutivo.
type KPIPayload struct {
	Year               int     `json:"year"`
	Month              int     `json:"month"`
	IngresosActual     float64 `json:"ingresos_actual"`
	IngresosAnterior   float64 `json:"ingresos_anterior"`
	IngresosVariacion  float64 `json:"ingresos_variacion"`
	EgresosActual      float64 `json:"egresos_actual"`
	EgresosAnterior    float64 `json:"egresos_anterior"`
	EgresosVariacion   float64 `json:"egresos_variacion"`
	// Otros campos se añadirán después (Resultado, Margen, etc.)
	LastUpdate         time.Time `json:"last_update"`
}

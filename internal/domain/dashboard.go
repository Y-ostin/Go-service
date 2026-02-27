// Package domain — tipos de datos para el dashboard financiero ejecutivo.
package domain

// DashboardFilter contiene los filtros año/mes que aplican a todas las
// consultas del dashboard.
type DashboardFilter struct {
	Year  int
	Month int
}

// IngresosResult representa el total de ingresos mensuales.
type IngresosResult struct {
	TotalIngresos float64 `json:"total_ingresos"`
}

// GastosResult representa el total de gastos mensuales
// (general_expenses + payment_approvals).
type GastosResult struct {
	TotalGastos float64 `json:"total_gastos"`
}

// ResultadoResult representa el resultado neto mensual (Ingresos – Gastos).
type ResultadoResult struct {
	Resultado float64 `json:"resultado"`
}

// MargenResult representa el margen de utilidad en porcentaje.
type MargenResult struct {
	Margen float64 `json:"margen"`
}

// CostLineItem representa una línea de costos con su monto e incidencia porcentual
// sobre el total de ingresos.
type CostLineItem struct {
	Name       string  `json:"name"`
	Total      float64 `json:"total"`
	Porcentaje float64 `json:"porcentaje"`
}

// ComposicionItem representa un concepto de gasto con su monto e incidencia
// porcentual sobre el total de gastos.
type ComposicionItem struct {
	Concepto   string  `json:"concepto"`
	Total      float64 `json:"total"`
	Porcentaje float64 `json:"porcentaje"`
}

// PuntoEquilibrioResult representa el análisis de punto de equilibrio mensual.
type PuntoEquilibrioResult struct {
	TotalIngresos   float64 `json:"total_ingresos"`
	TotalGastos     float64 `json:"total_gastos"`
	PuntoEquilibrio float64 `json:"punto_equilibrio"`
}

// AlertaEjecutivaResult representa la línea de costos con mayor participación
// porcentual en los ingresos del mes.
type AlertaEjecutivaResult struct {
	Name       string  `json:"name"`
	Porcentaje float64 `json:"porcentaje"`
}

// RiesgoItem representa la variación de gasto de una línea de costos
// respecto al mes anterior.
type RiesgoItem struct {
	CostLine  string  `json:"cost_line"`
	Variacion float64 `json:"variacion"`
}

// RatioResult representa el ratio gasto / ingreso en porcentaje.
type RatioResult struct {
	Ratio float64 `json:"ratio"`
}

package db

import (
	"context"
	"database/sql"
	"fmt"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/domain"
)

// MySQLDashboardRepository implementa domain.DashboardRepository sobre MariaDB.
// Todas las consultas son de solo lectura (SELECT) y filtradas por año y mes.
// No se crean tablas, vistas ni procedimientos almacenados.
type MySQLDashboardRepository struct {
	db  *sql.DB
	log *zap.Logger
}

// NewMySQLDashboardRepository construye el repositorio con el pool de conexiones inyectado.
func NewMySQLDashboardRepository(db *sql.DB, log *zap.Logger) *MySQLDashboardRepository {
	return &MySQLDashboardRepository{db: db, log: log}
}

// ── 1. Ingresos mensuales ─────────────────────────────────────────────────────

// GetIngresos devuelve la suma de ingresos del mes filtrado.
func (r *MySQLDashboardRepository) GetIngresos(ctx context.Context, f domain.DashboardFilter) (*domain.IngresosResult, error) {
	const q = `
		SELECT IFNULL(SUM(cca.amount), 0)
		FROM cicsa_charge_areas cca
		WHERE YEAR(cca.invoice_date) = ? AND MONTH(cca.invoice_date) = ?`

	var total float64
	if err := r.db.QueryRowContext(ctx, q, f.Year, f.Month).Scan(&total); err != nil {
		return nil, fmt.Errorf("GetIngresos: %w", err)
	}
	return &domain.IngresosResult{TotalIngresos: total}, nil
}

// ── 2. Gastos mensuales ───────────────────────────────────────────────────────

// GetGastos devuelve la suma de gastos del mes (general_expenses + payment_approvals).
func (r *MySQLDashboardRepository) GetGastos(ctx context.Context, f domain.DashboardFilter) (*domain.GastosResult, error) {
	const q = `
		SELECT
			(SELECT IFNULL(SUM(ge.amount), 0)
			 FROM general_expenses ge
			 WHERE YEAR(ge.operation_date) = ? AND MONTH(ge.operation_date) = ?)
			+
			(SELECT IFNULL(SUM(pa.amount), 0)
			 FROM payment_approvals pa
			 WHERE YEAR(pa.created_at) = ? AND MONTH(pa.created_at) = ?)
		AS total_gastos`

	var total float64
	if err := r.db.QueryRowContext(ctx, q, f.Year, f.Month, f.Year, f.Month).Scan(&total); err != nil {
		return nil, fmt.Errorf("GetGastos: %w", err)
	}
	return &domain.GastosResult{TotalGastos: total}, nil
}

// ── 3. Resultado (Utilidad / Pérdida) ─────────────────────────────────────────

// GetResultado devuelve el resultado neto mensual (ingresos - gastos).
func (r *MySQLDashboardRepository) GetResultado(ctx context.Context, f domain.DashboardFilter) (*domain.ResultadoResult, error) {
	const q = `
		SELECT
			(SELECT IFNULL(SUM(amount), 0)
			 FROM cicsa_charge_areas
			 WHERE YEAR(invoice_date) = ? AND MONTH(invoice_date) = ?)
			-
			(SELECT
				IFNULL(SUM(ge.amount), 0) +
				IFNULL((SELECT SUM(pa.amount)
				        FROM payment_approvals pa
				        WHERE YEAR(pa.created_at) = ? AND MONTH(pa.created_at) = ?), 0)
			 FROM general_expenses ge
			 WHERE YEAR(ge.operation_date) = ? AND MONTH(ge.operation_date) = ?)
		AS resultado`

	var resultado float64
	if err := r.db.QueryRowContext(ctx, q,
		f.Year, f.Month,
		f.Year, f.Month,
		f.Year, f.Month,
	).Scan(&resultado); err != nil {
		return nil, fmt.Errorf("GetResultado: %w", err)
	}
	return &domain.ResultadoResult{Resultado: resultado}, nil
}

// ── 4. Margen % ───────────────────────────────────────────────────────────────

// GetMargen devuelve el margen de utilidad porcentual del mes.
// Retorna 0 si los ingresos son cero (evita división por cero).
func (r *MySQLDashboardRepository) GetMargen(ctx context.Context, f domain.DashboardFilter) (*domain.MargenResult, error) {
	const q = `
		SELECT
			(ingresos.total_ingresos - gastos.total_gastos)
			/ NULLIF(ingresos.total_ingresos, 0) * 100 AS margen
		FROM
			(SELECT IFNULL(SUM(amount), 0) AS total_ingresos
			 FROM cicsa_charge_areas
			 WHERE YEAR(invoice_date) = ? AND MONTH(invoice_date) = ?) ingresos,
			(SELECT
				IFNULL((SELECT SUM(amount) FROM general_expenses
				        WHERE YEAR(operation_date) = ? AND MONTH(operation_date) = ?), 0)
				+
				IFNULL((SELECT SUM(amount) FROM payment_approvals
				        WHERE YEAR(created_at) = ? AND MONTH(created_at) = ?), 0)
			 AS total_gastos) gastos`

	var margen sql.NullFloat64
	if err := r.db.QueryRowContext(ctx, q,
		f.Year, f.Month,
		f.Year, f.Month,
		f.Year, f.Month,
	).Scan(&margen); err != nil {
		return nil, fmt.Errorf("GetMargen: %w", err)
	}
	return &domain.MargenResult{Margen: margen.Float64}, nil
}

// ── 5. Ingresos por cost line % ───────────────────────────────────────────────

// GetIngresosPorCostLine devuelve ingresos agrupados por cost_line con porcentaje.
func (r *MySQLDashboardRepository) GetIngresosPorCostLine(ctx context.Context, f domain.DashboardFilter) ([]domain.CostLineItem, error) {
	const q = `
		SELECT
			cl.name,
			SUM(cca.amount) AS total,
			SUM(cca.amount) /
				NULLIF((SELECT SUM(amount)
				        FROM cicsa_charge_areas
				        WHERE YEAR(invoice_date) = ? AND MONTH(invoice_date) = ?), 0) * 100 AS porcentaje
		FROM cicsa_charge_areas cca
		JOIN cicsa_assignations ca ON cca.cicsa_assignation_id = ca.id
		JOIN projects p  ON ca.project_id  = p.id
		JOIN cost_lines cl ON p.cost_line_id = cl.id
		WHERE YEAR(cca.invoice_date) = ? AND MONTH(cca.invoice_date) = ?
		GROUP BY cl.name
		ORDER BY total DESC`

	rows, err := r.db.QueryContext(ctx, q, f.Year, f.Month, f.Year, f.Month)
	if err != nil {
		return nil, fmt.Errorf("GetIngresosPorCostLine: %w", err)
	}
	defer rows.Close()

	var items []domain.CostLineItem
	for rows.Next() {
		var it domain.CostLineItem
		var pct sql.NullFloat64
		if err := rows.Scan(&it.Name, &it.Total, &pct); err != nil {
			return nil, fmt.Errorf("GetIngresosPorCostLine scan: %w", err)
		}
		it.Porcentaje = pct.Float64
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetIngresosPorCostLine rows: %w", err)
	}
	return items, nil
}

// ── 6. Composición de gastos ──────────────────────────────────────────────────

// GetComposicionGastos devuelve gastos agrupados por concepto (cost_line) con porcentaje.
func (r *MySQLDashboardRepository) GetComposicionGastos(ctx context.Context, f domain.DashboardFilter) ([]domain.ComposicionItem, error) {
	const q = `
		SELECT
			cl.name AS concepto,
			SUM(ge.amount) AS total,
			SUM(ge.amount) /
				NULLIF((SELECT SUM(amount)
				        FROM general_expenses
				        WHERE YEAR(operation_date) = ? AND MONTH(operation_date) = ?), 0) * 100 AS porcentaje
		FROM general_expenses ge
		JOIN cost_lines cl ON ge.cost_line_id = cl.id
		WHERE YEAR(ge.operation_date) = ? AND MONTH(ge.operation_date) = ?
		GROUP BY cl.name
		ORDER BY total DESC`

	rows, err := r.db.QueryContext(ctx, q, f.Year, f.Month, f.Year, f.Month)
	if err != nil {
		return nil, fmt.Errorf("GetComposicionGastos: %w", err)
	}
	defer rows.Close()

	var items []domain.ComposicionItem
	for rows.Next() {
		var it domain.ComposicionItem
		var pct sql.NullFloat64
		if err := rows.Scan(&it.Concepto, &it.Total, &pct); err != nil {
			return nil, fmt.Errorf("GetComposicionGastos scan: %w", err)
		}
		it.Porcentaje = pct.Float64
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetComposicionGastos rows: %w", err)
	}
	return items, nil
}

// ── 7. Punto de equilibrio ────────────────────────────────────────────────────

// GetPuntoEquilibrio devuelve ingresos, gastos y la diferencia (equilibrio).
func (r *MySQLDashboardRepository) GetPuntoEquilibrio(ctx context.Context, f domain.DashboardFilter) (*domain.PuntoEquilibrioResult, error) {
	const q = `
		SELECT
			ingresos.total_ingresos,
			gastos.total_gastos,
			ingresos.total_ingresos - gastos.total_gastos AS punto_equilibrio
		FROM
			(SELECT IFNULL(SUM(amount), 0) AS total_ingresos
			 FROM cicsa_charge_areas
			 WHERE YEAR(invoice_date) = ? AND MONTH(invoice_date) = ?) ingresos,
			(SELECT
				IFNULL((SELECT SUM(amount) FROM general_expenses
				        WHERE YEAR(operation_date) = ? AND MONTH(operation_date) = ?), 0)
				+
				IFNULL((SELECT SUM(amount) FROM payment_approvals
				        WHERE YEAR(created_at) = ? AND MONTH(created_at) = ?), 0)
			 AS total_gastos) gastos`

	var res domain.PuntoEquilibrioResult
	if err := r.db.QueryRowContext(ctx, q,
		f.Year, f.Month,
		f.Year, f.Month,
		f.Year, f.Month,
	).Scan(&res.TotalIngresos, &res.TotalGastos, &res.PuntoEquilibrio); err != nil {
		return nil, fmt.Errorf("GetPuntoEquilibrio: %w", err)
	}
	return &res, nil
}

// ── 8. Alerta ejecutiva ───────────────────────────────────────────────────────

// GetAlertaEjecutiva devuelve la cost_line con mayor participación en ingresos.
func (r *MySQLDashboardRepository) GetAlertaEjecutiva(ctx context.Context, f domain.DashboardFilter) (*domain.AlertaEjecutivaResult, error) {
	const q = `
		SELECT
			cl.name,
			SUM(cca.amount) /
				NULLIF((SELECT SUM(amount)
				        FROM cicsa_charge_areas
				        WHERE YEAR(invoice_date) = ? AND MONTH(invoice_date) = ?), 0) * 100 AS porcentaje
		FROM cicsa_charge_areas cca
		JOIN cicsa_assignations ca ON cca.cicsa_assignation_id = ca.id
		JOIN projects p  ON ca.project_id  = p.id
		JOIN cost_lines cl ON p.cost_line_id = cl.id
		WHERE YEAR(cca.invoice_date) = ? AND MONTH(cca.invoice_date) = ?
		GROUP BY cl.name
		ORDER BY porcentaje DESC
		LIMIT 1`

	var res domain.AlertaEjecutivaResult
	var pct sql.NullFloat64
	err := r.db.QueryRowContext(ctx, q, f.Year, f.Month, f.Year, f.Month).Scan(&res.Name, &pct)
	if err == sql.ErrNoRows {
		return &domain.AlertaEjecutivaResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetAlertaEjecutiva: %w", err)
	}
	res.Porcentaje = pct.Float64
	return &res, nil
}

// ── 9. Top 3 riesgos del mes (vs mes anterior) ────────────────────────────────

// GetTopRiesgos devuelve las 3 cost_lines con mayor variación de gasto
// respecto al mes anterior. Maneja el cambio de año (enero → diciembre anterior).
func (r *MySQLDashboardRepository) GetTopRiesgos(ctx context.Context, f domain.DashboardFilter) ([]domain.RiesgoItem, error) {
	// Calcular mes anterior con carry de año.
	prevYear, prevMonth := f.Year, f.Month-1
	if prevMonth == 0 {
		prevMonth = 12
		prevYear--
	}

	const q = `
		SELECT
			actual.cost_line,
			(actual.total - anterior.total) AS variacion
		FROM
			(SELECT cl.name AS cost_line, SUM(ge.amount) AS total
			 FROM general_expenses ge
			 JOIN cost_lines cl ON ge.cost_line_id = cl.id
			 WHERE YEAR(ge.operation_date) = ? AND MONTH(ge.operation_date) = ?
			 GROUP BY cl.name) actual
		JOIN
			(SELECT cl.name AS cost_line, SUM(ge.amount) AS total
			 FROM general_expenses ge
			 JOIN cost_lines cl ON ge.cost_line_id = cl.id
			 WHERE YEAR(ge.operation_date) = ? AND MONTH(ge.operation_date) = ?
			 GROUP BY cl.name) anterior
		ON actual.cost_line = anterior.cost_line
		ORDER BY ABS(actual.total - anterior.total) DESC
		LIMIT 3`

	rows, err := r.db.QueryContext(ctx, q, f.Year, f.Month, prevYear, prevMonth)
	if err != nil {
		return nil, fmt.Errorf("GetTopRiesgos: %w", err)
	}
	defer rows.Close()

	var items []domain.RiesgoItem
	for rows.Next() {
		var it domain.RiesgoItem
		if err := rows.Scan(&it.CostLine, &it.Variacion); err != nil {
			return nil, fmt.Errorf("GetTopRiesgos scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetTopRiesgos rows: %w", err)
	}
	return items, nil
}

// ── 10. Ratio gasto / ingreso ─────────────────────────────────────────────────

// GetRatio devuelve el ratio gastos/ingresos en porcentaje.
// Retorna 0 si los ingresos son cero.
func (r *MySQLDashboardRepository) GetRatio(ctx context.Context, f domain.DashboardFilter) (*domain.RatioResult, error) {
	const q = `
		SELECT
			gastos.total_gastos / NULLIF(ingresos.total_ingresos, 0) * 100 AS ratio
		FROM
			(SELECT IFNULL(SUM(amount), 0) AS total_ingresos
			 FROM cicsa_charge_areas
			 WHERE YEAR(invoice_date) = ? AND MONTH(invoice_date) = ?) ingresos,
			(SELECT
				IFNULL((SELECT SUM(amount) FROM general_expenses
				        WHERE YEAR(operation_date) = ? AND MONTH(operation_date) = ?), 0)
				+
				IFNULL((SELECT SUM(amount) FROM payment_approvals
				        WHERE YEAR(created_at) = ? AND MONTH(created_at) = ?), 0)
			 AS total_gastos) gastos`

	var ratio sql.NullFloat64
	if err := r.db.QueryRowContext(ctx, q,
		f.Year, f.Month,
		f.Year, f.Month,
		f.Year, f.Month,
	).Scan(&ratio); err != nil {
		return nil, fmt.Errorf("GetRatio: %w", err)
	}
	return &domain.RatioResult{Ratio: ratio.Float64}, nil
}

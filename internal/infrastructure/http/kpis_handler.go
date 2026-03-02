// kpis_handler.go  Endpoint REST /api/kpis y /api/kpis/periods
// Columnas reales:
//   cicsa_charge_areas  amount (double),  invoice_date (date nullable), created_at
//   general_expenses    amount (varchar), operation_date (date nullable), workflow_status ('done'/'draft'), created_at
// Se usa COALESCE porque invoice_date y operation_date son nullable.
package http

import (
"encoding/json"
"fmt"
"net/http"
"strconv"
"time"

"github.com/ccip/go-service/internal/domain"
"go.uber.org/zap"
"gorm.io/gorm"
)

// KPIHandler implementa /api/kpis y /api/kpis/periods.
type KPIHandler struct {
db  *gorm.DB
log *zap.Logger
}

func NewKPIHandler(db *gorm.DB, log *zap.Logger) *KPIHandler {
return &KPIHandler{db: db, log: log}
}

// scanFloat ejecuta un Raw query escalar y devuelve float64.
func (h *KPIHandler) scanFloat(query string, args ...interface{}) float64 {
var result float64
if err := h.db.Raw(query, args...).Scan(&result).Error; err != nil {
h.log.Warn("query KPI error", zap.Error(err))
}
return result
}

// ServeHTTP
//   GET /api/kpis                   → mes actual
//   GET /api/kpis?year=YYYY&month=M → mes específico  vs mes anterior
//   GET /api/kpis?year=YYYY         → resumen anual   vs año anterior
//   GET /api/kpis?year=0            → acumulado total (todos los años)
func (h *KPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Content-Type", "application/json")

yearStr  := r.URL.Query().Get("year")
monthStr := r.URL.Query().Get("month")

var year, month int
if yearStr == "" {
now := time.Now()
year, month = now.Year(), int(now.Month())
} else {
year, _ = strconv.Atoi(yearStr)
if monthStr != "" {
if m, err := strconv.Atoi(monthStr); err == nil && m >= 1 && m <= 12 {
month = m
}
// monthStr presente pero 0 o inválido → month=0 (modo anual)
}
// monthStr ausente → month=0 (modo anual)
}

// ── Queries ────────────────────────────────────────────────────────────────
const qIngresosAll = `SELECT IFNULL(SUM(amount),0)
  FROM cicsa_charge_areas
 WHERE YEAR(COALESCE(invoice_date, created_at)) >= 2020`

const qEgresosAll = `SELECT IFNULL(SUM(CAST(amount AS DECIMAL(15,2))),0)
  FROM general_expenses
 WHERE workflow_status = 'done'
   AND YEAR(COALESCE(operation_date, created_at)) >= 2020`

const qIngresosYear = `SELECT IFNULL(SUM(amount),0)
  FROM cicsa_charge_areas
 WHERE YEAR(COALESCE(invoice_date, created_at)) = ?`

const qEgresosYear = `SELECT IFNULL(SUM(CAST(amount AS DECIMAL(15,2))),0)
  FROM general_expenses
 WHERE workflow_status = 'done'
   AND YEAR(COALESCE(operation_date, created_at)) = ?`

const qIngresos = `SELECT IFNULL(SUM(amount),0)
  FROM cicsa_charge_areas
 WHERE YEAR(COALESCE(invoice_date, created_at))  = ?
   AND MONTH(COALESCE(invoice_date, created_at)) = ?`

const qEgresos = `SELECT IFNULL(SUM(CAST(amount AS DECIMAL(15,2))),0)
  FROM general_expenses
 WHERE workflow_status = 'done'
   AND YEAR(COALESCE(operation_date, created_at))  = ?
   AND MONTH(COALESCE(operation_date, created_at)) = ?`

var ingresosActual, ingresosAnterior, ingresosVar float64
var egresosActual, egresosAnterior, egresosVar float64

switch {
case year == 0:
// Modo total acumulado — sin comparativa
year, month = 0, 0
ingresosActual = h.scanFloat(qIngresosAll)
egresosActual  = h.scanFloat(qEgresosAll)

case month == 0:
// Modo anual: comparar con el año anterior
ingresosActual   = h.scanFloat(qIngresosYear, year)
ingresosAnterior = h.scanFloat(qIngresosYear, year-1)
if ingresosAnterior > 0 {
ingresosVar = ((ingresosActual - ingresosAnterior) / ingresosAnterior) * 100
}
egresosActual   = h.scanFloat(qEgresosYear, year)
egresosAnterior = h.scanFloat(qEgresosYear, year-1)
if egresosAnterior > 0 {
egresosVar = ((egresosActual - egresosAnterior) / egresosAnterior) * 100
}

default:
// Modo mensual: comparar con el mes anterior
prevYear, prevMonth := year, month-1
if prevMonth == 0 {
prevMonth, prevYear = 12, year-1
}
ingresosActual   = h.scanFloat(qIngresos, year, month)
ingresosAnterior = h.scanFloat(qIngresos, prevYear, prevMonth)
if ingresosAnterior > 0 {
ingresosVar = ((ingresosActual - ingresosAnterior) / ingresosAnterior) * 100
}
egresosActual   = h.scanFloat(qEgresos, year, month)
egresosAnterior = h.scanFloat(qEgresos, prevYear, prevMonth)
if egresosAnterior > 0 {
egresosVar = ((egresosActual - egresosAnterior) / egresosAnterior) * 100
}
}

payload := domain.KPIPayload{
Year:              year,
Month:             month,
IngresosActual:    ingresosActual,
IngresosAnterior:  ingresosAnterior,
IngresosVariacion: ingresosVar,
EgresosActual:     egresosActual,
EgresosAnterior:   egresosAnterior,
EgresosVariacion:  egresosVar,
LastUpdate:        time.Now(),
}

h.log.Debug("KPI calculado",
zap.Int("year", year), zap.Int("month", month),
zap.Float64("ingresos", ingresosActual),
zap.Float64("egresos", egresosActual),
)

_ = json.NewEncoder(w).Encode(payload)
}

// ServeBreakdownHTTP  GET /api/kpis/breakdown?year=YYYY&month=M
// Devuelve composición de ingresos por cost_line y egresos por expense_type.
func (h *KPIHandler) ServeBreakdownHTTP(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Content-Type", "application/json")

yearStr  := r.URL.Query().Get("year")
monthStr := r.URL.Query().Get("month")

var year, month int
if yearStr == "" {
now := time.Now()
year, month = now.Year(), int(now.Month())
} else {
year, _ = strconv.Atoi(yearStr)
if monthStr != "" {
if m, err := strconv.Atoi(monthStr); err == nil && m >= 1 && m <= 12 {
month = m
}
}
}

type Slice struct {
Label string  `json:"label"`
Value float64 `json:"value"`
}

// ── Filtro de fecha reutilizable ───────────────────────────────────────────
// Construye el fragmento WHERE + args según modo (total / anual / mensual)
buildDateFilter := func(yearCol, monthCol string) (string, []interface{}) {
switch {
case year == 0:
return fmt.Sprintf("YEAR(%s) >= 2020", yearCol), nil
case month == 0:
return fmt.Sprintf("YEAR(%s) = ?", yearCol), []interface{}{year}
default:
return fmt.Sprintf("YEAR(%s) = ? AND MONTH(%s) = ?", yearCol, monthCol), []interface{}{year, month}
}
}

// ── Ingresos por cost_line ─────────────────────────────────────────────────
// cicsa_charge_areas → cicsa_assignations → projects → cost_lines.name
dateFieldI  := "COALESCE(ca.invoice_date, ca.created_at)"
whereI, argsI := buildDateFilter(dateFieldI, dateFieldI)

qIngByLine := fmt.Sprintf(`
SELECT COALESCE(cl.name,'Sin asignar') AS label,
       IFNULL(SUM(ca.amount),0)        AS value
  FROM cicsa_charge_areas ca
  LEFT JOIN cicsa_assignations ass ON ass.id = ca.cicsa_assignation_id
  LEFT JOIN projects p              ON p.id  = ass.project_id
  LEFT JOIN cost_lines cl           ON cl.id = p.cost_line_id
 WHERE %s
 GROUP BY cl.name
 ORDER BY value DESC
`, whereI)

var ingresos []Slice
if err := h.db.Raw(qIngByLine, argsI...).Scan(&ingresos).Error; err != nil {
h.log.Warn("breakdown ingresos error", zap.Error(err))
}

// ── Egresos por expense_type ───────────────────────────────────────────────
dateFieldE  := "COALESCE(operation_date, created_at)"
whereE, argsE := buildDateFilter(dateFieldE, dateFieldE)

qEgByType := fmt.Sprintf(`
SELECT COALESCE(NULLIF(TRIM(expense_type),''),'Sin tipo') AS label,
       IFNULL(SUM(CAST(amount AS DECIMAL(15,2))),0)        AS value
  FROM general_expenses
 WHERE workflow_status = 'done'
   AND %s
 GROUP BY label
 ORDER BY value DESC
`, whereE)

var egresos []Slice
if err := h.db.Raw(qEgByType, argsE...).Scan(&egresos).Error; err != nil {
h.log.Warn("breakdown egresos error", zap.Error(err))
}

_ = json.NewEncoder(w).Encode(map[string]interface{}{
"ingresos": ingresos,
"egresos":  egresos,
})
}

// ServePeriodsHTTP  GET /api/kpis/periods
// Devuelve lista de {year,month} con data disponible, ordenados desc.
func (h *KPIHandler) ServePeriodsHTTP(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Content-Type", "application/json")

type Period struct {
Year  int `json:"year"`
Month int `json:"month"`
}

var periods []Period
err := h.db.Raw(`
SELECT yr AS year, mo AS month FROM (
SELECT YEAR(COALESCE(invoice_date, created_at))  AS yr,
       MONTH(COALESCE(invoice_date, created_at)) AS mo
  FROM cicsa_charge_areas
 WHERE amount > 0
   AND YEAR(COALESCE(invoice_date, created_at)) >= 2020
UNION
SELECT YEAR(COALESCE(operation_date, created_at)),
       MONTH(COALESCE(operation_date, created_at))
  FROM general_expenses
 WHERE workflow_status = 'done'
   AND YEAR(COALESCE(operation_date, created_at)) >= 2020
) t
GROUP BY yr, mo
ORDER BY yr DESC, mo DESC
`).Scan(&periods).Error

if err != nil {
h.log.Error("error obteniendo períodos", zap.Error(err))
http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
return
}

_ = json.NewEncoder(w).Encode(periods)
}

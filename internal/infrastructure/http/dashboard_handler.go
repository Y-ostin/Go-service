package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/application"
	"github.com/ccip/go-service/internal/domain"
)

// DashboardHandler expone los 10 endpoints del dashboard financiero ejecutivo.
// Cada handler es stateless: lee query params, delega al servicio y serializa JSON.
type DashboardHandler struct {
	svc *application.DashboardService
	log *zap.Logger
}

// NewDashboardHandler construye el handler con el servicio inyectado.
func NewDashboardHandler(svc *application.DashboardService, log *zap.Logger) *DashboardHandler {
	return &DashboardHandler{svc: svc, log: log}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parseFilter extrae y valida los query params ?anio=&mes= del request.
func parseFilter(r *http.Request) (domain.DashboardFilter, error) {
	q := r.URL.Query()
	anioStr := q.Get("anio")
	mesStr := q.Get("mes")
	if anioStr == "" || mesStr == "" {
		return domain.DashboardFilter{}, fmt.Errorf("parámetros 'anio' y 'mes' son requeridos")
	}
	anio, err := strconv.Atoi(anioStr)
	if err != nil {
		return domain.DashboardFilter{}, fmt.Errorf("'anio' debe ser un número entero válido")
	}
	mes, err := strconv.Atoi(mesStr)
	if err != nil {
		return domain.DashboardFilter{}, fmt.Errorf("'mes' debe ser un número entero válido")
	}
	return domain.DashboardFilter{Year: anio, Month: mes}, nil
}

// writeJSON serializa v como JSON con Content-Type application/json.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// En este punto los headers ya fueron enviados; solo loguear.
		_ = err
	}
}

// writeError envía una respuesta de error JSON con el código HTTP indicado.
func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// HandleIngresos → GET /dashboard/ingresos?anio=&mes=
func (h *DashboardHandler) HandleIngresos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetIngresos(r.Context(), f)
	if err != nil {
		h.log.Error("GetIngresos", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando ingresos")
		return
	}
	writeJSON(w, res)
}

// HandleGastos → GET /dashboard/gastos?anio=&mes=
func (h *DashboardHandler) HandleGastos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetGastos(r.Context(), f)
	if err != nil {
		h.log.Error("GetGastos", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando gastos")
		return
	}
	writeJSON(w, res)
}

// HandleResultado → GET /dashboard/resultado?anio=&mes=
func (h *DashboardHandler) HandleResultado(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetResultado(r.Context(), f)
	if err != nil {
		h.log.Error("GetResultado", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando resultado")
		return
	}
	writeJSON(w, res)
}

// HandleMargen → GET /dashboard/margen?anio=&mes=
func (h *DashboardHandler) HandleMargen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetMargen(r.Context(), f)
	if err != nil {
		h.log.Error("GetMargen", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando margen")
		return
	}
	writeJSON(w, res)
}

// HandleIngresosCostLine → GET /dashboard/ingresos-cost-line?anio=&mes=
func (h *DashboardHandler) HandleIngresosCostLine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetIngresosPorCostLine(r.Context(), f)
	if err != nil {
		h.log.Error("GetIngresosPorCostLine", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando ingresos por cost line")
		return
	}
	if res == nil {
		res = []domain.CostLineItem{}
	}
	writeJSON(w, res)
}

// HandleComposicionGastos → GET /dashboard/composicion-gastos?anio=&mes=
func (h *DashboardHandler) HandleComposicionGastos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetComposicionGastos(r.Context(), f)
	if err != nil {
		h.log.Error("GetComposicionGastos", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando composición de gastos")
		return
	}
	if res == nil {
		res = []domain.ComposicionItem{}
	}
	writeJSON(w, res)
}

// HandlePuntoEquilibrio → GET /dashboard/punto-equilibrio?anio=&mes=
func (h *DashboardHandler) HandlePuntoEquilibrio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetPuntoEquilibrio(r.Context(), f)
	if err != nil {
		h.log.Error("GetPuntoEquilibrio", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando punto de equilibrio")
		return
	}
	writeJSON(w, res)
}

// HandleAlertaEjecutiva → GET /dashboard/alerta-ejecutiva?anio=&mes=
func (h *DashboardHandler) HandleAlertaEjecutiva(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetAlertaEjecutiva(r.Context(), f)
	if err != nil {
		h.log.Error("GetAlertaEjecutiva", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando alerta ejecutiva")
		return
	}
	writeJSON(w, res)
}

// HandleTopRiesgos → GET /dashboard/top-riesgos?anio=&mes=
func (h *DashboardHandler) HandleTopRiesgos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetTopRiesgos(r.Context(), f)
	if err != nil {
		h.log.Error("GetTopRiesgos", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando top riesgos")
		return
	}
	if res == nil {
		res = []domain.RiesgoItem{}
	}
	writeJSON(w, res)
}

// HandleRatio → GET /dashboard/ratio?anio=&mes=
func (h *DashboardHandler) HandleRatio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "método no permitido")
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.GetRatio(r.Context(), f)
	if err != nil {
		h.log.Error("GetRatio", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "error consultando ratio")
		return
	}
	writeJSON(w, res)
}

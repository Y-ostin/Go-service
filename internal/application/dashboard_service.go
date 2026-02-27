package application

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/domain"
)

// DashboardService orquesta los casos de uso del dashboard financiero.
// Valida los parámetros de entrada y delega la consulta al DashboardRepository.
type DashboardService struct {
	repo domain.DashboardRepository
	log  *zap.Logger
}

// NewDashboardService construye el servicio con el repositorio inyectado.
func NewDashboardService(repo domain.DashboardRepository, log *zap.Logger) *DashboardService {
	return &DashboardService{repo: repo, log: log}
}

// validateFilter verifica que año y mes estén en rangos válidos.
func (s *DashboardService) validateFilter(f domain.DashboardFilter) error {
	if f.Year < 2000 || f.Year > 2100 {
		return fmt.Errorf("año inválido: %d (debe estar entre 2000 y 2100)", f.Year)
	}
	if f.Month < 1 || f.Month > 12 {
		return fmt.Errorf("mes inválido: %d (debe estar entre 1 y 12)", f.Month)
	}
	return nil
}

// GetIngresos devuelve el total de ingresos del mes.
func (s *DashboardService) GetIngresos(ctx context.Context, f domain.DashboardFilter) (*domain.IngresosResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetIngresos(ctx, f)
}

// GetGastos devuelve el total de gastos del mes (general_expenses + payment_approvals).
func (s *DashboardService) GetGastos(ctx context.Context, f domain.DashboardFilter) (*domain.GastosResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetGastos(ctx, f)
}

// GetResultado devuelve el resultado neto del mes (ingresos − gastos).
func (s *DashboardService) GetResultado(ctx context.Context, f domain.DashboardFilter) (*domain.ResultadoResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetResultado(ctx, f)
}

// GetMargen devuelve el margen de utilidad porcentual del mes.
func (s *DashboardService) GetMargen(ctx context.Context, f domain.DashboardFilter) (*domain.MargenResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetMargen(ctx, f)
}

// GetIngresosPorCostLine devuelve ingresos desglosados por cost_line con porcentajes.
func (s *DashboardService) GetIngresosPorCostLine(ctx context.Context, f domain.DashboardFilter) ([]domain.CostLineItem, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetIngresosPorCostLine(ctx, f)
}

// GetComposicionGastos devuelve gastos agrupados por concepto con porcentajes.
func (s *DashboardService) GetComposicionGastos(ctx context.Context, f domain.DashboardFilter) ([]domain.ComposicionItem, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetComposicionGastos(ctx, f)
}

// GetPuntoEquilibrio devuelve el análisis de punto de equilibrio del mes.
func (s *DashboardService) GetPuntoEquilibrio(ctx context.Context, f domain.DashboardFilter) (*domain.PuntoEquilibrioResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetPuntoEquilibrio(ctx, f)
}

// GetAlertaEjecutiva devuelve la cost_line con mayor participación en ingresos.
func (s *DashboardService) GetAlertaEjecutiva(ctx context.Context, f domain.DashboardFilter) (*domain.AlertaEjecutivaResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetAlertaEjecutiva(ctx, f)
}

// GetTopRiesgos devuelve las 3 cost_lines con mayor variación de gasto vs el mes anterior.
func (s *DashboardService) GetTopRiesgos(ctx context.Context, f domain.DashboardFilter) ([]domain.RiesgoItem, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetTopRiesgos(ctx, f)
}

// GetRatio devuelve el ratio gasto/ingreso en porcentaje.
func (s *DashboardService) GetRatio(ctx context.Context, f domain.DashboardFilter) (*domain.RatioResult, error) {
	if err := s.validateFilter(f); err != nil {
		return nil, err
	}
	return s.repo.GetRatio(ctx, f)
}

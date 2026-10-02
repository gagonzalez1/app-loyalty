package service

import (
	"clientesFrecuentes/internal/analytics"
	"clientesFrecuentes/internal/model"
	"context"
	"errors"
	"time"
)

func (s *Service) BrandPeriodMetrics(ctx context.Context, actorID, brandID int64, period, date string) (model.BrandPeriodMetrics, error) {
	if analytics.Validate(period, date) != nil {
		return model.BrandPeriodMetrics{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	out, err := s.Repo.BrandPeriodMetrics(ctx, actorID, brandID, period, date, now)
	if errors.Is(err, analytics.ErrInvalidRequest) {
		return model.BrandPeriodMetrics{}, ErrInvalidRequest
	}
	return out, err
}

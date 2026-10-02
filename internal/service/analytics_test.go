package service

import (
	"context"
	"errors"
	"testing"
)

func TestPeriodMetricsValidateBeforeRepository(t *testing.T) {
	s := &Service{}
	for _, tt := range [][2]string{{"", ""}, {"daily", ""}, {"week", "2026-02-30"}, {"month", "2026-1-01"}} {
		if _, err := s.BrandPeriodMetrics(context.Background(), 1, 1, tt[0], tt[1]); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%v: %v", tt, err)
		}
	}
}

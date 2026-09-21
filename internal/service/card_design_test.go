package service

import (
	"context"
	"errors"
	"testing"

	"clientesFrecuentes/internal/model"
)

func TestUpdateBrandRejectsUnknownCardDesignValues(t *testing.T) {
	svc := &Service{}
	unknownTemplate := "UNKNOWN_TEMPLATE"
	if _, err := svc.UpdateBrand(context.Background(), 1, 1, 1, model.UpdateBrandRequest{CardTemplate: &unknownTemplate}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown template error=%v", err)
	}
	unsafeIcon := "../../logo"
	if _, err := svc.UpdateBrand(context.Background(), 1, 1, 1, model.UpdateBrandRequest{RewardImage: &unsafeIcon}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe reward icon error=%v", err)
	}
}

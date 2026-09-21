package service

import (
	"testing"

	"clientesFrecuentes/internal/config"
)

func TestSubscriptionUnitPriceByProgram(t *testing.T) {
	service := Service{Config: config.Config{MercadoPagoBranchPrice: 1500000, MercadoPagoPointsPrice: 2000000}}

	for _, test := range []struct {
		program string
		want    int64
		ok      bool
	}{
		{program: "SELLOS", want: 1500000, ok: true},
		{program: "PUNTOS", want: 2000000, ok: true},
		{program: "DESCONOCIDO", ok: false},
	} {
		got, ok := service.subscriptionUnitPrice(test.program)
		if got != test.want || ok != test.ok {
			t.Fatalf("subscriptionUnitPrice(%q) = (%d, %t), want (%d, %t)", test.program, got, ok, test.want, test.ok)
		}
	}
}

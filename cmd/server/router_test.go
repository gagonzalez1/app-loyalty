package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/service"
)

// Fix the external route inventory and check authentication on every protected
// route: moving registration between groups must not silently expose a domain.
func TestRouteInventoryAndAuthentication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler.Handler{Service: &service.Service{}, Limiter: middleware.NewRateLimiter(), Logger: logger}
	r := newRouter(h, auth.NewTokens("01234567890123456789012345678901", "puntazo"), logger)
	public := []string{
		"GET /v1/health/live", "GET /v1/health/ready", "GET /v1/version",
		"POST /v1/auth/register", "POST /v1/auth/login", "POST /v1/auth/google", "POST /v1/auth/refresh", "POST /v1/demo/comercios",
		"POST /v1/auth/email-verification/request", "POST /v1/auth/email-verification/confirm",
		"POST /v1/auth/password-reset/request", "POST /v1/auth/password-reset/confirm",
		"GET /v1/invitaciones/:token", "POST /v1/invitaciones/:token/registrar",
		"POST /v1/mercado-pago/webhooks",
		"POST /auth/register", "POST /auth/login", "POST /auth/google",
	}
	protected := []struct{ method, pattern, request string }{
		{"GET", "/v1/me", "/v1/me"},
		{"PATCH", "/v1/me", "/v1/me"},
		{"GET", "/v1/me/export", "/v1/me/export"},
		{"DELETE", "/v1/me", "/v1/me"},
		{"POST", "/v1/auth/logout", "/v1/auth/logout"},
		{"GET", "/v1/me/foto", "/v1/me/foto"},
		{"POST", "/v1/me/foto", "/v1/me/foto"},
		{"GET", "/me", "/me"},
		{"GET", "/v1/marcas", "/v1/marcas"},
		{"GET", "/v1/marcas/:brand_id", "/v1/marcas/1"},
		{"PATCH", "/v1/marcas/:brand_id", "/v1/marcas/1"},
		{"DELETE", "/v1/marcas/:brand_id", "/v1/marcas/1"},
		{"GET", "/v1/marcas/:brand_id/sucursales", "/v1/marcas/1/sucursales"},
		{"POST", "/v1/marcas/:brand_id/sucursales", "/v1/marcas/1/sucursales"},
		{"GET", "/v1/marcas/:brand_id/sucursales/:resource_id", "/v1/marcas/1/sucursales/1"},
		{"PATCH", "/v1/marcas/:brand_id/sucursales/:resource_id", "/v1/marcas/1/sucursales/1"},
		{"DELETE", "/v1/marcas/:brand_id/sucursales/:resource_id", "/v1/marcas/1/sucursales/1"},
		{"GET", "/v1/marcas/:brand_id/programa", "/v1/marcas/1/programa"},
		{"PUT", "/v1/marcas/:brand_id/programa", "/v1/marcas/1/programa"},
		{"GET", "/v1/marcas/:brand_id/beneficios", "/v1/marcas/1/beneficios"},
		{"POST", "/v1/marcas/:brand_id/beneficios", "/v1/marcas/1/beneficios"},
		{"GET", "/v1/marcas/:brand_id/beneficios/:resource_id", "/v1/marcas/1/beneficios/1"},
		{"PUT", "/v1/marcas/:brand_id/beneficios/:resource_id", "/v1/marcas/1/beneficios/1"},
		{"PATCH", "/v1/marcas/:brand_id/beneficios/:resource_id", "/v1/marcas/1/beneficios/1"},
		{"DELETE", "/v1/marcas/:brand_id/beneficios/:resource_id", "/v1/marcas/1/beneficios/1"},
		{"GET", "/v1/marcas/:brand_id/movimientos", "/v1/marcas/1/movimientos"},
		{"GET", "/v1/marcas/:brand_id/clientes", "/v1/marcas/1/clientes"},
		{"GET", "/v1/marcas/:brand_id/metricas/resumen", "/v1/marcas/1/metricas/resumen"},
		{"GET", "/v1/marcas/:brand_id/imagenes", "/v1/marcas/1/imagenes"},
		{"POST", "/v1/marcas/:brand_id/imagenes", "/v1/marcas/1/imagenes"},
		{"DELETE", "/v1/marcas/:brand_id/imagenes/:image_id", "/v1/marcas/1/imagenes/00000000-0000-0000-0000-000000000001"},
		{"GET", "/v1/marcas/:brand_id/invitaciones", "/v1/marcas/1/invitaciones"},
		{"POST", "/v1/marcas/:brand_id/invitaciones", "/v1/marcas/1/invitaciones"},
		{"POST", "/v1/marcas/:brand_id/invitaciones/:invitation_id/reenviar", "/v1/marcas/1/invitaciones/00000000-0000-0000-0000-000000000001/reenviar"},
		{"DELETE", "/v1/marcas/:brand_id/invitaciones/:invitation_id", "/v1/marcas/1/invitaciones/00000000-0000-0000-0000-000000000001"},
		{"GET", "/v1/marcas/:brand_id/personal", "/v1/marcas/1/personal"},
		{"GET", "/v1/marcas/:brand_id/personal/:membership_id", "/v1/marcas/1/personal/1"},
		{"PATCH", "/v1/marcas/:brand_id/personal/:membership_id", "/v1/marcas/1/personal/1"},
		{"DELETE", "/v1/marcas/:brand_id/personal/:membership_id", "/v1/marcas/1/personal/1"},
		{"GET", "/v1/marcas/:brand_id/suscripcion", "/v1/marcas/1/suscripcion"},
		{"POST", "/v1/marcas/:brand_id/suscripcion/checkout", "/v1/marcas/1/suscripcion/checkout"},
		{"POST", "/v1/marcas/:brand_id/suscripcion/cancelacion", "/v1/marcas/1/suscripcion/cancelacion"},
		{"GET", "/v1/clientes/me", "/v1/clientes/me"},
		{"GET", "/v1/clientes/me/tarjetas", "/v1/clientes/me/tarjetas"},
		{"POST", "/v1/clientes/me/push-token", "/v1/clientes/me/push-token"},
		{"GET", "/v1/clientes/me/movimientos", "/v1/clientes/me/movimientos"},
		{"GET", "/v1/clientes/me/tarjetas/:card_id/movimientos", "/v1/clientes/me/tarjetas/1/movimientos"},
		{"POST", "/v1/movimientos/preview", "/v1/movimientos/preview"},
		{"POST", "/v1/movimientos/scan", "/v1/movimientos/scan"},
		{"POST", "/v1/movimientos/canje", "/v1/movimientos/canje"},
		{"GET", "/v1/movimientos/idempotencia/:idempotency_key", "/v1/movimientos/idempotencia/test"},
	}
	expected := make(map[string]bool)
	for _, route := range public {
		expected[route] = true
	}
	for _, route := range protected {
		expected[route.method+" "+route.pattern] = true
		t.Run(route.method+" "+route.request, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(route.method, route.request, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if !expected[key] {
			t.Errorf("unexpected route %s", key)
		}
		delete(expected, key)
	}
	for key := range expected {
		t.Errorf("missing route %s", key)
	}
}

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

func TestPushTokenRouteRequiresAuthentication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler.Handler{Service: &service.Service{}, Limiter: middleware.NewRateLimiter(), Logger: logger}
	r := newRouter(h, auth.NewTokens("01234567890123456789012345678901", "puntazo"), logger)

	found := false
	for _, route := range r.Routes() {
		if route.Method == http.MethodPost && route.Path == "/v1/clientes/me/push-token" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("POST /v1/clientes/me/push-token is not registered")
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/clientes/me/push-token", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: got %d, want 401: %s", w.Code, w.Body.String())
	}
}

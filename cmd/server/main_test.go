package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/service"
)

func TestPublicHealthAndVersionContract(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &service.Service{Config: config.Config{AppVersion: "0.1.0", GitCommit: "abcdef0", ExpectedSchemaVersion: "0001"}}
	h := &handler.Handler{Service: svc, Limiter: middleware.NewRateLimiter(), Logger: logger}
	r := newRouter(h, auth.NewTokens("01234567890123456789012345678901", "puntazo"), logger)
	for _, path := range []string{"/v1/health/live", "/v1/version"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status %d", path, w.Code)
		}
	}
}

func TestAuthRequiredUsesStableEnvelope(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler.Handler{Service: &service.Service{}, Limiter: middleware.NewRateLimiter(), Logger: logger}
	r := newRouter(h, auth.NewTokens("01234567890123456789012345678901", "puntazo"), logger)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["request_id"] == nil || body["error"] == nil {
		t.Fatalf("invalid envelope %s", w.Body.String())
	}
}

func TestStrictJSONRejectsUnknownAndOversized(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler.Handler{Service: &service.Service{}, Limiter: middleware.NewRateLimiter(), Logger: logger}
	r := newRouter(h, auth.NewTokens("01234567890123456789012345678901", "puntazo"), logger)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/register", bytes.NewBufferString(`{"email":"a@example.com","password":"1234567890","name":"A","role":"ADMIN"}`))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field status %d body %s", w.Code, w.Body.String())
	}
	large := bytes.Repeat([]byte("x"), (1<<20)+1)
	request = httptest.NewRequest(http.MethodPost, "/v1/auth/register", bytes.NewReader(large))
	request.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("large status %d", w.Code)
	}
}

func TestCustomerRegistrationIsClosedWhenDemoSignupIsDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler.Handler{
		Service: &service.Service{Config: config.Config{DemoSignupEnabled: false}},
		Limiter: middleware.NewRateLimiter(),
		Logger:  logger,
	}
	r := newRouter(h, auth.NewTokens("01234567890123456789012345678901", "puntazo"), logger)

	for _, path := range []string{"/v1/auth/register", "/auth/register"} {
		t.Run(path, func(t *testing.T) {
			requestBody := `{"email":"customer@example.com","password":"customer-pass","name":"Customer"}`
			if path == "/auth/register" {
				requestBody = `{"email":"customer@example.com","password":"customer-pass","nombre":"Customer"}`
			}
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(requestBody))
			req.Header.Set("Content-Type", "application/json")
			if path == "/v1/auth/register" {
				req.Header.Set("X-Client-Platform", "native")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var responseBody struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &responseBody); err != nil {
				t.Fatal(err)
			}
			if responseBody.Error.Code != "DEMO_SIGNUP_DISABLED" {
				t.Fatalf("error code = %q", responseBody.Error.Code)
			}
		})
	}
}

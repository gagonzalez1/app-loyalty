package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the real route groups with current sessions and an isolated schema.
// Retiring the 18+ gate must neither invent declarations nor bypass suspension.
func TestPostgresAccessWithoutHistoricalAdultDeclaration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "test_age_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS citext; CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `CREATE TABLE schema_migrations(version CHAR(4) PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, strings.Replace(string(body), "CREATE EXTENSION IF NOT EXISTS citext;", "", 1)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, filepath.Base(path)[:4]); err != nil {
			t.Fatal(err)
		}
	}
	repo := repository.New(pool)
	tokens := auth.NewTokens("01234567890123456789012345678901", "puntazo")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handler.Handler{Repo: repo, Service: service.New(repo, tokens, config.Config{}), Limiter: middleware.NewRateLimiter(), Logger: logger}
	router := newRouter(h, tokens, logger)
	for _, kind := range []string{"CLIENTE_FINAL", "PERSONAL_MARCA"} {
		t.Run(kind, func(t *testing.T) {
			var id int64
			err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash) VALUES($1,'Age policy test',$2,CASE WHEN $2='CLIENTE_FINAL' THEN $3::bytea ELSE NULL END) RETURNING id`, uuid.NewString()+"@example.test", kind, []byte(uuid.NewString())).Scan(&id)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			session := uuid.NewString()
			if err = repo.CreateSession(ctx, session, id, []byte(uuid.NewString()), now.Add(time.Hour), now, 1); err != nil {
				t.Fatal(err)
			}
			token, err := tokens.GenerateForSessionAtVersion(id, kind, session, now, 1)
			if err != nil {
				t.Fatal(err)
			}
			request := func(path string) *httptest.ResponseRecorder {
				response := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				router.ServeHTTP(response, req)
				return response
			}
			paths := []string{"/v1/me", "/me"}
			if kind == "CLIENTE_FINAL" {
				paths = append(paths, "/v1/clientes/me/tarjetas")
			} else {
				paths = append(paths, "/v1/marcas")
			}
			for _, path := range paths {
				if response := request(path); response.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
				}
			}
			if confirmed, err := repo.AdultConfirmed(ctx, id); err != nil || confirmed {
				t.Fatalf("declaration was invented: %v %v", confirmed, err)
			}
			if _, err = pool.Exec(ctx, `UPDATE usuarios SET activo=false WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if response := request(paths[len(paths)-1]); response.Code != http.StatusUnauthorized {
				t.Fatalf("suspended account: %d", response.Code)
			}
		})
	}
}

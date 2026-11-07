package repository_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/repository"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestPostgresAppOpeningWithExistingSession(t *testing.T) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	repo := repository.New(pool)
	var userID int64
	err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash)
		VALUES('opening@example.test','Opening Fixture','CLIENTE_FINAL',decode(md5('opening'),'hex')) RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}

	tokens := auth.NewTokens("01234567890123456789012345678901", "puntazo")
	sessionID := uuid.NewString()
	authTime := time.Now().Add(-time.Hour)
	if err := repo.CreateSession(ctx, sessionID, userID, []byte("opening-session"), time.Now().Add(time.Hour), authTime, 1); err != nil {
		t.Fatal(err)
	}
	token, err := tokens.GenerateForSessionAtVersion(userID, "CLIENTE_FINAL", sessionID, authTime, 1)
	if err != nil {
		t.Fatal(err)
	}
	h := &handler.Handler{Repo: repo, Limiter: middleware.NewRateLimiter()}
	r := gin.New()
	r.POST("/v1/me/app-open", middleware.RequireAuth(tokens, repo), h.RecordAppOpening)
	open := func(token, body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/app-open", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	registeredUser := func() repository.BackofficeRegisteredUser {
		t.Helper()
		page, err := repo.ListBackofficeRegisteredUsers(ctx, "opening@example.test", "", 5, 0)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		return page.Items[0]
	}

	body := fmt.Sprintf(`{"user_id":%d}`, userID)
	if got := open("", body); got != 401 {
		t.Fatalf("no session: HTTP %d", got)
	}
	if got := open(token, fmt.Sprintf(`{"user_id":%d}`, userID+1)); got != 403 {
		t.Fatalf("different account: HTTP %d", got)
	}
	if got := open(token, `{"user_id":1,"opened_at":"2030-01-01"}`); got != 422 {
		t.Fatalf("client timestamp: HTTP %d", got)
	}
	if got := registeredUser().LastAppOpenedAt; got != nil {
		t.Fatalf("creating or rejecting a session request recorded an opening: %s", got)
	}
	if got := open(token, body); got != 204 {
		t.Fatalf("first opening: HTTP %d", got)
	}
	first := registeredUser().LastAppOpenedAt
	if first == nil || !first.After(authTime) {
		t.Fatalf("first opening=%v", first)
	}
	if got := open(token, body); got != 204 {
		t.Fatalf("reopen with same session: HTTP %d", got)
	}
	last := registeredUser().LastAppOpenedAt
	if last == nil || !last.After(*first) {
		t.Fatalf("first=%v last=%v", first, last)
	}
	exported, err := repo.ExportAccount(ctx, userID)
	if err != nil || exported.LastAppOpenedAt == nil || !exported.LastAppOpenedAt.Equal(*last) {
		t.Fatalf("exported opening=%v err=%v", exported.LastAppOpenedAt, err)
	}
	if _, err := repo.AnonymizeAccount(ctx, userID, exported.User.Version); err != nil {
		t.Fatal(err)
	}
	if got := open(token, body); got != 401 {
		t.Fatalf("anonymized account: HTTP %d", got)
	}
	if err := pool.QueryRow(ctx, `SELECT last_app_opened_at FROM usuarios WHERE id=$1`, userID).Scan(&last); err != nil || last != nil {
		t.Fatalf("anonymized opening=%v err=%v", last, err)
	}
}

package repository_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestPostgresCustomerStoreGoogleReview(t *testing.T) {
	p := referralBillingPool(t)
	ctx := t.Context()
	f := fixtureReviews(t, p)
	repo := repository.New(p)
	svc := service.New(repo, nil, config.Config{})
	// This customer has neither a card nor a previous purchase at this branch.
	var cards int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM tarjetas WHERE usuario_id=$1`, f.other).Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("card-free fixture=%d err=%v", cards, err)
	}
	out, err := svc.CustomerStoreGoogleReview(ctx, f.other, f.branch)
	if err != nil || out.BranchID != f.branch || out.ReviewURL != nil {
		t.Fatalf("unconfigured=%+v err=%v", out, err)
	}
	in := enabledReviewInput(1)
	in.Enabled = false
	if _, err = repo.SaveReviewSettings(ctx, f.owner, f.brand, f.branch, 1, in); err != nil {
		t.Fatal(err)
	}
	invitation := uuid.NewString()
	if _, err = p.Exec(ctx, `INSERT INTO invitaciones_resenas(id,usuario_id,tarjeta_id,marca_id,sucursal_id,occurred_at) VALUES($1,$2,$3,$4,$5,now())`, invitation, f.customer, f.card, f.brand, f.branch); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var state string
		if err := p.QueryRow(ctx, `SELECT jsonb_build_object('settings',(SELECT to_jsonb(c) FROM configuracion_resenas c WHERE sucursal_id=$1),'invitations',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitaciones_resenas i),'progress',(SELECT jsonb_agg(to_jsonb(p) ORDER BY usuario_id,sucursal_id) FROM progreso_resenas p))::text`, f.branch).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	for _, actor := range []int64{f.other, f.customer} {
		out, err = svc.CustomerStoreGoogleReview(ctx, actor, f.branch)
		if err != nil || out.ReviewURL == nil || *out.ReviewURL != *in.ManualReviewURL {
			t.Fatalf("disabled automatic invitations=%+v err=%v", out, err)
		}
	}
	if after := snapshot(); before != after {
		t.Fatal("manual destination read mutated settings, progress or invitations")
	}
	if _, err = repo.CustomerStoreReviewSettings(ctx, f.owner, f.branch); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("merchant actor err=%v", err)
	}
	h := &handler.Handler{Repo: repo, Service: svc, Limiter: middleware.NewRateLimiter()}
	request := func(actor int64, kind string, branch int64) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.ActorKey, middleware.Actor{ID: actor, AccountType: kind}) }, h.RequireAdult)
		r.GET("/tiendas/:branch_id/resena-google", h.CustomerStoreGoogleReview)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest("GET", fmt.Sprintf("/tiendas/%d/resena-google", branch), nil))
		return response
	}
	if response := request(f.other, "CLIENTE_FINAL", f.branch); response.Code != 403 || !strings.Contains(response.Body.String(), "ADULT_CONFIRMATION_REQUIRED") {
		t.Fatalf("adult gate status=%d body=%s", response.Code, response.Body.String())
	}
	if err = repo.ConfirmAdult(ctx, f.other); err != nil {
		t.Fatal(err)
	}
	response := request(f.other, "CLIENTE_FINAL", f.branch)
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || len(envelope.Data) != 2 || envelope.Data["review_url"] == nil || envelope.Data["branch_id"] == nil {
		t.Fatalf("minimal response status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request(f.other, "CLIENTE_FINAL", 9223372036854775807); response.Code != 404 {
		t.Fatalf("missing branch status=%d", response.Code)
	}
	for _, state := range []struct{ table, column, value, reset string }{
		{"sucursales", "activo", "false", "true"}, {"sucursales", "deleted_at", "now()", "NULL"},
		{"marcas", "activo", "false", "true"}, {"marcas", "deleted_at", "now()", "NULL"},
		{"usuarios", "activo", "false", "true"}, {"usuarios", "deleted_at", "now()", "NULL"},
	} {
		id := f.branch
		if state.table == "marcas" {
			id = f.brand
		} else if state.table == "usuarios" {
			id = f.other
		}
		if _, err = p.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s=%s WHERE id=$1", state.table, state.column, state.value), id); err != nil {
			t.Fatal(err)
		}
		if _, err = repo.CustomerStoreReviewSettings(ctx, f.other, f.branch); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("ineligible %s.%s err=%v", state.table, state.column, err)
		}
		if _, err = p.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s=%s WHERE id=$1", state.table, state.column, state.reset), id); err != nil {
			t.Fatal(err)
		}
	}
	placeID := "ChIJ_test"
	in.DestinationType, in.GooglePlaceID, in.ManualReviewURL = "GOOGLE_PLACE", &placeID, nil
	if _, err = repo.SaveReviewSettings(ctx, f.owner, f.brand, f.branch, 2, in); err != nil {
		t.Fatal(err)
	}
	svc.Places = &reviewProviderFake{fail: true}
	response = request(f.other, "CLIENTE_FINAL", f.branch)
	if response.Code != 503 || !strings.Contains(response.Body.String(), `"code":"DEPENDENCY_UNAVAILABLE"`) {
		t.Fatalf("provider failure status=%d body=%s", response.Code, response.Body.String())
	}
}

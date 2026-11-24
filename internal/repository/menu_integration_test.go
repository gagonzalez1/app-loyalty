package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/maintenance"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type menuTestStore struct {
	mu         sync.Mutex
	objects    map[string][]byte
	failPut    atomic.Bool
	failSign   atomic.Bool
	failDelete atomic.Bool
	maxTTL     time.Duration
	entered    chan struct{}
	release    chan struct{}
}

func (m *menuTestStore) Put(ctx context.Context, key, mime string, b, d []byte) error {
	if m.entered != nil {
		m.entered <- struct{}{}
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.failPut.Load() {
		return errors.New("storage failure")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), b...)
	return nil
}
func (m *menuTestStore) Delete(_ context.Context, key string) error {
	if m.failDelete.Load() {
		return errors.New("delete failure")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
func (m *menuTestStore) SignedGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	if m.failSign.Load() {
		return "", errors.New("sign failure")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ttl > m.maxTTL {
		m.maxTTL = ttl
	}
	return "https://media.example.test/private/" + key + "?signed=qa", nil
}
func (m *menuTestStore) ReadEmailImage(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("not used")
}
func (m *menuTestStore) Ready(context.Context) error { return nil }
func menuPDF(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../service/testdata/menu.pdf")
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func menuFixture(t *testing.T) (*pgxpool.Pool, *repository.Repository, *service.Service, *menuTestStore, reviewFixture) {
	t.Helper()
	p := referralBillingPool(t)
	f := fixtureReviews(t, p)
	repo := repository.New(p)
	store := &menuTestStore{objects: map[string][]byte{}}
	svc := service.New(repo, nil, config.Config{MediaURLTTL: time.Hour}, store)
	return p, repo, svc, store, f
}
func uploadMenu(t *testing.T, s *service.Service, f reviewFixture, v int) model.BranchMenuState {
	t.Helper()
	out, err := s.UploadMenuDocument(t.Context(), f.owner, f.brand, f.branch, model.MenuSpanish, v, "../../Carta.pdf", menuPDF(t))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPostgresBranchPDFMenuLifecycle(t *testing.T) {
	p, repo, svc, store, f := menuFixture(t)
	ctx := t.Context()
	initial, err := svc.BranchMenu(ctx, f.owner, f.brand, f.branch, model.MenuSpanish)
	if err != nil || initial.Version != 1 || initial.Draft != nil || initial.Published != nil {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	var rows int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM branch_menus`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("GET wrote rows=%d err=%v", rows, err)
	}
	for _, actor := range []int64{f.operator, f.customer} {
		if _, err = svc.BranchMenu(ctx, actor, f.brand, f.branch, model.MenuSpanish); err == nil {
			t.Fatal("unauthorized private GET")
		}
	}
	if _, err = svc.BranchMenu(ctx, f.owner, f.brand+999, f.branch, model.MenuSpanish); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("tenant mismatch=%v", err)
	}
	draft := uploadMenu(t, svc, f, 1)
	if draft.Version != 2 || draft.Draft == nil || draft.Published != nil || draft.Draft.FileName != "Carta.pdf" || draft.Draft.PageCount != 1 {
		t.Fatalf("draft=%+v", draft)
	}
	public, err := svc.PublicBranchMenu(ctx, f.branch, model.MenuSpanish)
	if err != nil || public.Published != nil {
		t.Fatalf("draft leaked=%+v err=%v", public, err)
	}
	published, err := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 2, "publish", draft.Draft.ID)
	if err != nil || published.Version != 3 || published.Draft != nil || published.Published == nil {
		t.Fatalf("publish=%+v err=%v", published, err)
	}
	catalog, err := repo.ListCustomerStoreLocations(ctx, f.customer, "Main")
	if err != nil || len(catalog) != 1 || !catalog[0].HasMenu {
		t.Fatalf("published menu missing from customer catalog: %+v err=%v", catalog, err)
	}
	original := published.Published.ID
	if store.maxTTL > 300*time.Second || store.maxTTL <= 0 {
		t.Fatalf("ttl=%s", store.maxTTL)
	}
	if published.Published.URLExpiresAt.Sub(svc.Now()) > 300*time.Second {
		t.Fatal("URL exceeds TTL")
	}
	public, err = svc.PublicBranchMenu(ctx, f.branch, model.MenuSpanish)
	if err != nil || public.Published == nil || public.Published.ID != original {
		t.Fatalf("public=%+v err=%v", public, err)
	}
	listed, err := svc.PublicBrandMenus(ctx, f.brand)
	if err != nil || len(listed) != 1 || !listed[0].HasMenu || listed[0].Latitude != nil {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	encoded, _ := json.Marshal(public)
	for _, secret := range []string{"object_key", "draft", "usuario_id", "email", "saldo"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public leaked %s", encoded)
		}
	}
	if _, err = svc.UploadMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 2, "new.pdf", menuPDF(t)); !errors.Is(err, repository.ErrPreconditionFailed) {
		t.Fatalf("stale=%v", err)
	}
	if _, err = svc.UploadMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 3, "fake.pdf", []byte("%PDF-1.4 fake")); !errors.Is(err, service.ErrMenuPDFInvalid) {
		t.Fatalf("fake=%v", err)
	}
	store.failPut.Store(true)
	if _, err = svc.UploadMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 3, "new.pdf", menuPDF(t)); !errors.Is(err, service.ErrMenuStorage) {
		t.Fatalf("put fail=%v", err)
	}
	store.failPut.Store(false)
	store.failSign.Store(true)
	if _, err = svc.UploadMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 3, "new.pdf", menuPDF(t)); !errors.Is(err, service.ErrMenuStorage) {
		t.Fatalf("sign fail=%v", err)
	}
	store.failSign.Store(false)
	state, err := svc.BranchMenu(ctx, f.owner, f.brand, f.branch, model.MenuSpanish)
	if err != nil || state.Version != 3 || state.Published.ID != original || state.Draft != nil {
		t.Fatalf("failed replacement changed state=%+v err=%v", state, err)
	}
	next := uploadMenu(t, svc, f, 3)
	store.failSign.Store(true)
	if _, err = svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 4, "publish", next.Draft.ID); !errors.Is(err, service.ErrMenuStorage) {
		t.Fatalf("publish sign fail=%v", err)
	}
	store.failSign.Store(false)
	state, err = svc.BranchMenu(ctx, f.owner, f.brand, f.branch, model.MenuSpanish)
	if err != nil || state.Version != 4 || state.Published.ID != original || state.Draft.ID != next.Draft.ID {
		t.Fatalf("failed publish changed state=%+v err=%v", state, err)
	}
	state, err = svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 4, "publish", next.Draft.ID)
	if err != nil || state.Version != 5 || state.Published.ID != next.Draft.ID {
		t.Fatalf("replacement=%+v err=%v", state, err)
	}
	var status string
	var after time.Time
	if err = p.QueryRow(ctx, `SELECT estado,delete_after FROM branch_menu_documents WHERE id=$1`, original).Scan(&status, &after); err != nil || status != "DELETE_PENDING" || time.Until(after) < 290*time.Second {
		t.Fatalf("grace status=%s after=%s err=%v", status, after, err)
	}
	state, err = svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 5, "unpublish", "")
	if err != nil || state.Version != 6 || state.Published != nil {
		t.Fatalf("withdraw=%+v err=%v", state, err)
	}
	state, err = svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 6, "unpublish", "")
	if err != nil || state.Version != 6 {
		t.Fatalf("repeat withdraw=%+v err=%v", state, err)
	}
	state = uploadMenu(t, svc, f, 6)
	state, err = svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 7, "discard", "")
	if err != nil || state.Version != 8 || state.Draft != nil {
		t.Fatalf("discard=%+v err=%v", state, err)
	}
	public, err = svc.PublicBranchMenu(ctx, f.branch, model.MenuSpanish)
	if err != nil || public.Published != nil {
		t.Fatalf("withdrawn=%+v err=%v", public, err)
	}
	if _, err = repo.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 8, "publish", uuid.NewString(), nil); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other document=%v", err)
	}
	// Existing administrative role can manage PDFs, operators cannot.
	if _, err = p.Exec(ctx, `UPDATE membresias_marca SET rol='ADMINISTRADOR' WHERE usuario_id=$1 AND marca_id=$2`, f.operator, f.brand); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.BranchMenu(ctx, f.operator, f.brand, f.branch, model.MenuSpanish); err != nil {
		t.Fatalf("admin=%v", err)
	}
}

func TestPostgresBranchPDFMenuConcurrentReplacement(t *testing.T) {
	p, _, svc, store, f := menuFixture(t)
	ctx := t.Context()
	first := uploadMenu(t, svc, f, 1)
	current, err := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 2, "publish", first.Draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.entered = make(chan struct{}, 2)
	store.release = make(chan struct{})
	result := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, e := svc.UploadMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 3, "replacement.pdf", menuPDF(t))
			result <- e
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-store.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("upload did not reach storage")
		}
	}
	close(store.release)
	var successes, conflicts int
	for i := 0; i < 2; i++ {
		err = <-result
		if err == nil {
			successes++
		} else if errors.Is(err, repository.ErrPreconditionFailed) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", successes, conflicts)
	}
	state, err := svc.BranchMenu(ctx, f.owner, f.brand, f.branch, model.MenuSpanish)
	if err != nil || state.Version != 4 || state.Published.ID != current.Published.ID || state.Draft == nil {
		t.Fatalf("race state=%+v err=%v", state, err)
	}
	for _, action := range []string{"publish", "unpublish"} {
		go func(action string) {
			_, e := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 4, action, state.Draft.ID)
			result <- e
		}(action)
	}
	successes, conflicts = 0, 0
	for i := 0; i < 2; i++ {
		err = <-result
		if err == nil {
			successes++
		} else if errors.Is(err, repository.ErrPreconditionFailed) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("publication race success=%d conflicts=%d", successes, conflicts)
	}
	var n int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM branch_menu_documents WHERE estado='UPLOAD_PENDING'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pending=%d err=%v", n, err)
	}
}

func TestPostgresBranchPDFMenuCleanupAndLifecycle(t *testing.T) {
	for _, lifecycle := range []string{"branch", "brand", "owner", "shared-owner"} {
		t.Run(lifecycle, func(t *testing.T) {
			p, repo, svc, store, f := menuFixture(t)
			ctx := t.Context()
			draft := uploadMenu(t, svc, f, 1)
			if _, err := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 2, "publish", draft.Draft.ID); err != nil {
				t.Fatal(err)
			}
			_ = uploadMenu(t, svc, f, 3)
			switch lifecycle {
			case "branch":
				if _, err := p.Exec(ctx, `UPDATE sucursales SET activo=false,deleted_at=now() WHERE id=$1`, f.branch); err != nil {
					t.Fatal(err)
				}
			case "brand":
				if err := repo.DeleteBrand(ctx, f.owner, f.brand, 1); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if _, err := repo.AnonymizeAccount(ctx, f.owner, 1); err != nil {
					t.Fatal(err)
				}
			case "shared-owner":
				if _, err := p.Exec(ctx, `UPDATE membresias_marca SET rol='PROPIETARIO' WHERE usuario_id=$1 AND marca_id=$2`, f.operator, f.brand); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.AnonymizeAccount(ctx, f.owner, 1); err != nil {
					t.Fatal(err)
				}
				public, err := svc.PublicBranchMenu(ctx, f.branch, model.MenuSpanish)
				if err != nil || public.Published == nil {
					t.Fatalf("shared brand PDF lost=%+v err=%v", public, err)
				}
				return
			}
			if _, err := svc.PublicBranchMenu(ctx, f.branch, model.MenuSpanish); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("closed branch=%v", err)
			}
			var pointers int
			if err := p.QueryRow(ctx, `SELECT count(*) FROM branch_menus WHERE draft_document_id IS NOT NULL OR published_document_id IS NOT NULL`).Scan(&pointers); err != nil || pointers != 0 {
				t.Fatalf("pointers=%d err=%v", pointers, err)
			}
			if _, err := p.Exec(ctx, `UPDATE branch_menu_documents SET delete_after=now()-interval '1 second' WHERE estado='DELETE_PENDING'`); err != nil {
				t.Fatal(err)
			}
			worker := maintenance.Worker{Repo: repo, Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: config.Config{RetentionBatchSize: 50}}
			store.failDelete.Store(true)
			worker.RunMediaCleanup(ctx)
			var retries int
			if err := p.QueryRow(ctx, `SELECT count(*) FROM branch_menu_documents WHERE estado='DELETE_PENDING' AND lease_owner IS NULL AND delete_after>now()`).Scan(&retries); err != nil || retries != 2 {
				t.Fatalf("retries=%d err=%v", retries, err)
			}
			store.failDelete.Store(false)
			if _, err := p.Exec(ctx, `UPDATE branch_menu_documents SET delete_after=now()-interval '1 second' WHERE estado='DELETE_PENDING'`); err != nil {
				t.Fatal(err)
			}
			worker.RunMediaCleanup(ctx)
			var completed int
			if err := p.QueryRow(ctx, `SELECT count(*) FROM branch_menu_documents WHERE estado='DELETED'`).Scan(&completed); err != nil || completed != 2 {
				t.Fatalf("completed=%d err=%v", completed, err)
			}
		})
	}
}

func TestPostgresBranchPDFMenuHTTPContract(t *testing.T) {
	_, repo, svc, _, f := menuFixture(t)
	h := &handler.Handler{Repo: repo, Service: svc, Limiter: middleware.NewRateLimiter(), Uploads: middleware.NewUploadSemaphore(2, 1)}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ActorKey, middleware.Actor{ID: f.owner, AccountType: "PERSONAL_MARCA"})
	})
	r.GET("/menu", func(c *gin.Context) {
		c.Params = append(c.Params, gin.Param{Key: "brand_id", Value: fmt.Sprint(f.brand)}, gin.Param{Key: "resource_id", Value: fmt.Sprint(f.branch)})
		h.BranchMenu(c)
	})
	for _, route := range []struct {
		method, path string
		handler      gin.HandlerFunc
	}{{"POST", "/menu/documentos", h.UploadMenuDocument}, {"PUT", "/menu/publicacion", h.PublishBranchMenu}, {"DELETE", "/menu/publicacion", h.UnpublishBranchMenu}, {"DELETE", "/menu/borrador", h.DiscardMenuDraft}} {
		fn := route.handler
		r.Handle(route.method, route.path, func(c *gin.Context) {
			c.Params = append(c.Params, gin.Param{Key: "brand_id", Value: fmt.Sprint(f.brand)}, gin.Param{Key: "resource_id", Value: fmt.Sprint(f.branch)})
			fn(c)
		})
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		path := "/menu/publicacion"
		if method == "POST" {
			path = "/menu/documentos"
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 428 {
			t.Fatalf("missing match %s=%d %s", method, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/menu", nil))
	if w.Code != 200 || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("initial=%d %s", w.Code, w.Body.String())
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("archivo", "Carta.pdf")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(menuPDF(t))
	form.Close()
	req := httptest.NewRequest("POST", "/menu/documentos", &body)
	req.Header.Set("If-Match", `"1"`)
	req.Header.Set("Content-Type", form.FormDataContentType())
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 201 || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("upload=%d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data model.BranchMenuState `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("PUT", "/menu/publicacion", strings.NewReader(`{"document_id":"`+envelope.Data.Draft.ID+`"}`))
	req.Header.Set("If-Match", `"2"`)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("ETag") != `"3"` {
		t.Fatalf("publish=%d %s", w.Code, w.Body.String())
	}
}

func TestPostgresBranchPDFMenuMigrationUpgrade(t *testing.T) {
	p := referralBillingPool(t)
	ctx := t.Context()
	rollbackCodeMigrationFixture(t, p)
	newerDown, err := os.ReadFile("../../migrations/0042_branch_pdf_30mib.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(newerDown)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0042'`); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/0041_branch_pdf_menus.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0041'`); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(p)
	if err = repo.CheckSchema(ctx, "0040"); err != nil {
		t.Fatal(err)
	}
	f := fixtureReviews(t, p)
	up, err := os.ReadFile("../../migrations/0041_branch_pdf_menus.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		var applied bool
		if err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='0041')`).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied {
			continue
		}
		tx, err := p.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(up)); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0041')`); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CheckSchema(ctx, "0041"); err != nil {
		t.Fatal(err)
	}
	var menus int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM branch_menus WHERE sucursal_id=$1`, f.branch).Scan(&menus); err != nil || menus != 0 {
		t.Fatalf("migration created a menu unexpectedly: count=%d err=%v", menus, err)
	}
	var cards int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM tarjetas WHERE id=$1`, f.card).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("card lost=%d err=%v", cards, err)
	}
}

func TestPostgresMenuCompensationWaitsForActivation(t *testing.T) {
	p, repo, _, _, f := menuFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	document := model.MenuDocument{ID: uuid.NewString(), FileName: "menu.pdf", ByteSize: 1, PageCount: 1, ObjectKey: "menus/ambiguous.pdf"}
	if err := repo.ReserveMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 1, document, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	activation, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Rollback(context.Background())
	var pid int
	if err = activation.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = activation.Exec(ctx, `UPDATE branch_menu_documents SET estado='READY' WHERE id=$1`, document.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = activation.Exec(ctx, `INSERT INTO branch_menus(sucursal_id,marca_id,version,draft_document_id) VALUES($1,$2,2,$3)`, f.branch, f.brand, document.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- repo.FailMenuUpload(ctx, document.ID) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("compensation did not wait for activation: %v", e)
		case <-ctx.Done():
			t.Fatal("compensation never waited")
		case <-ticker.C:
		}
	}
	if err = activation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var status string
	if err = p.QueryRow(ctx, `SELECT estado FROM branch_menu_documents WHERE id=$1`, document.ID).Scan(&status); err != nil || status != "READY" {
		t.Fatalf("committed draft retired: status=%s err=%v", status, err)
	}
	state, err := repo.BranchMenu(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, nil)
	if err != nil || state.Draft == nil || state.Draft.ID != document.ID {
		t.Fatalf("draft lost=%+v err=%v", state, err)
	}
}

func TestPostgresMenuPublicSigningProtectsDeletionGrace(t *testing.T) {
	p, repo, svc, _, f := menuFixture(t)
	ctx := t.Context()
	draft := uploadMenu(t, svc, f, 1)
	if _, err := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 2, "publish", draft.Draft.ID); err != nil {
		t.Fatal(err)
	}
	signing := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var urlExpiry time.Time
	go func() {
		_, err := repo.PublicBranchMenu(ctx, f.branch, model.MenuSpanish, func(_ context.Context, d *model.MenuDocument) error {
			close(signing)
			<-release
			urlExpiry = time.Now().Add(300 * time.Second)
			d.URL = "https://media.example.test/document.pdf"
			d.URLExpiresAt = urlExpiry
			return nil
		})
		done <- err
	}()
	<-signing
	// A withdrawal must wait until the public request finishes signing. This
	// bounded context proves the lock instead of relying on a scheduling sleep.
	blocked, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err := svc.ChangeMenuPublication(blocked, f.owner, f.brand, f.branch, model.MenuSpanish, 3, "unpublish", "")
	cancel()
	if err == nil {
		close(release)
		t.Fatal("withdrawal passed while signing held the publication")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 3, "unpublish", ""); err != nil {
		t.Fatal(err)
	}
	var deletion time.Time
	if err = p.QueryRow(ctx, `SELECT delete_after FROM branch_menu_documents WHERE id=$1`, draft.Draft.ID).Scan(&deletion); err != nil {
		t.Fatal(err)
	}
	if deletion.Before(urlExpiry.Add(50 * time.Second)) {
		t.Fatalf("deletion=%s does not cover expiry=%s plus margin", deletion, urlExpiry)
	}
}

func TestPostgresMenuAbandonedUploadLeaseAndRetry(t *testing.T) {
	p, repo, _, _, f := menuFixture(t)
	ctx := t.Context()
	document := model.MenuDocument{ID: uuid.NewString(), FileName: "menu.pdf", ByteSize: 1, PageCount: 1, ObjectKey: "menus/abandoned.pdf"}
	if err := repo.ReserveMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, 1, document, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE branch_menu_documents SET created_at=now()-interval '16 minutes' WHERE id=$1`, document.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.DueMenuMedia(ctx, 1, "lease-a")
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	second, err := repo.DueMenuMedia(ctx, 1, "lease-b")
	if err != nil || len(second) != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err = repo.CompleteMenuMediaDeletion(ctx, document.ID, "lease-b", false); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
	if err = repo.CompleteMenuMediaDeletion(ctx, document.ID, "lease-a", true); err != nil {
		t.Fatal(err)
	}
	delayed, err := repo.DueMenuMedia(ctx, 1, "lease-c")
	if err != nil || len(delayed) != 0 {
		t.Fatalf("retry too early=%+v err=%v", delayed, err)
	}
	if _, err = p.Exec(ctx, `UPDATE branch_menu_documents SET delete_after=now()-interval '1 second' WHERE id=$1`, document.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := repo.DueMenuMedia(ctx, 1, "lease-c")
	if err != nil || len(retried) != 1 {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	if err = repo.CompleteMenuMediaDeletion(ctx, document.ID, "lease-c", false); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMenuPDFThirtyMiBUpgradePreservesPublication(t *testing.T) {
	p, repo, _, _, f := menuFixture(t)
	ctx := t.Context()
	rollbackCodeMigrationFixture(t, p)
	// Arrange the applied 0041 schema, including an actual existing publication.
	down, err := os.ReadFile("../../migrations/0042_branch_pdf_30mib.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0042'`); err != nil {
		t.Fatal(err)
	}
	publicationID := uuid.NewString()
	if _, err = p.Exec(ctx, `INSERT INTO branch_menu_documents(id,marca_id,sucursal_id,file_name,byte_size,page_count,sha256,object_key,estado) VALUES($1,$2,$3,'carta.pdf',1024,1,$4,$5,'READY')`, publicationID, f.brand, f.branch, make([]byte, 32), "menus/"+publicationID+".pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO branch_menus(sucursal_id,marca_id,version,published_document_id) VALUES($1,$2,3,$3)`, f.branch, f.brand, publicationID); err != nil {
		t.Fatal(err)
	}
	if err = repo.CheckSchema(ctx, "0041"); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/0042_branch_pdf_30mib.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Same version-based idempotency as the migrator: do not execute applied SQL twice.
	for pass := 0; pass < 2; pass++ {
		var applied bool
		if err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='0042')`).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied {
			continue
		}
		tx, err := p.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(up)); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0042')`); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CheckSchema(ctx, "0042"); err != nil {
		t.Fatal(err)
	}
	var version int
	var id string
	if err = p.QueryRow(ctx, `SELECT version,published_document_id::text FROM branch_menus WHERE sucursal_id=$1`, f.branch).Scan(&version, &id); err != nil || version != 3 || id != publicationID {
		t.Fatalf("publication changed during expansion: version=%d id=%s err=%v", version, id, err)
	}
	const maximum int64 = 30 * 1024 * 1024
	for _, size := range []int64{0, 1, maximum, maximum + 1} {
		id := uuid.NewString()
		document := model.MenuDocument{ID: id, FileName: "scan.pdf", MIMEType: "application/pdf", ByteSize: size, PageCount: 1, ObjectKey: "menus/" + id + ".pdf"}
		_, err := p.Exec(ctx, `INSERT INTO branch_menu_documents(id,marca_id,sucursal_id,file_name,byte_size,page_count,sha256,object_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, document.ID, f.brand, f.branch, document.FileName, document.ByteSize, document.PageCount, make([]byte, 32), document.ObjectKey)
		if size == 0 || size > maximum {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "branch_menu_documents_byte_size_check" {
				t.Fatalf("size=%d must fail DB size CHECK: %v", size, err)
			}
		} else if err != nil {
			t.Fatalf("size=%d must fit DB bound: %v", size, err)
		}
	}
	// Narrowing must preserve larger metadata instead of truncating/removing it.
	if _, err = p.Exec(ctx, string(down)); err == nil {
		t.Fatal("unsafe down accepted a >10 MiB document")
	}
	var large int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM branch_menu_documents WHERE byte_size=$1`, maximum).Scan(&large); err != nil || large != 1 {
		t.Fatalf("large metadata lost after rejected down: count=%d err=%v", large, err)
	}
}

func TestPostgresMenuPDFPageCountExpansion(t *testing.T) {
	p, repo, svc, _, f := menuFixture(t)
	ctx := t.Context()
	up, err := os.ReadFile("../../migrations/0047_branch_pdf_no_page_cap.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/0047_branch_pdf_no_page_cap.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	draft := uploadMenu(t, svc, f, 1)
	before, err := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuSpanish, draft.Version, "publish", draft.Draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	after, err := svc.BranchMenu(ctx, f.owner, f.brand, f.branch, model.MenuSpanish)
	if err != nil || after.Version != before.Version || after.Published == nil || after.Published.ID != before.Published.ID {
		t.Fatalf("publication changed during expansion: before=%+v after=%+v err=%v", before, after, err)
	}
	body, err := os.ReadFile("../service/testdata/menu-31.pdf")
	if err != nil {
		t.Fatal(err)
	}
	long, err := svc.UploadMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuEnglish, 1, "menu-en.pdf", body)
	if err != nil || long.Draft == nil || long.Draft.PageCount != 31 {
		t.Fatalf("31-page menu was not stored: state=%+v err=%v", long, err)
	}
	published, err := svc.ChangeMenuPublication(ctx, f.owner, f.brand, f.branch, model.MenuEnglish, long.Version, "publish", long.Draft.ID)
	if err != nil || published.Published == nil || published.Published.PageCount != 31 {
		t.Fatalf("31-page menu was not published: state=%+v err=%v", published, err)
	}
	spanish, err := svc.PublicBranchMenu(ctx, f.branch, model.MenuSpanish)
	if err != nil || spanish.Published == nil || spanish.Published.ID != before.Published.ID {
		t.Fatalf("English publication changed Spanish menu: state=%+v err=%v", spanish, err)
	}
	english, err := svc.PublicBranchMenu(ctx, f.branch, model.MenuEnglish)
	if err != nil || english.Published == nil || english.Published.ID != published.Published.ID {
		t.Fatalf("English PDF unavailable: state=%+v err=%v", english, err)
	}
	catalog, err := repo.ListCustomerStoreLocations(ctx, f.customer, "")
	if err != nil || len(catalog) != 1 || !catalog[0].HasMenu || !catalog[0].HasEnglishMenu {
		t.Fatalf("catalog does not show both published PDFs: %+v err=%v", catalog, err)
	}
	if _, err = p.Exec(ctx, string(down)); err == nil {
		t.Fatal("unsafe down accepted an existing 31-page document")
	}
	var pages int
	if err = p.QueryRow(ctx, `SELECT page_count FROM branch_menu_documents WHERE id=$1`, published.Published.ID).Scan(&pages); err != nil || pages != 31 {
		t.Fatalf("31-page document changed after failed down: pages=%d err=%v", pages, err)
	}
	id := uuid.NewString()
	empty := model.MenuDocument{ID: id, FileName: "empty.pdf", MIMEType: "application/pdf", ByteSize: 1, PageCount: 0, ObjectKey: "menus/" + id + ".pdf"}
	var pgErr *pgconn.PgError
	if err = repo.ReserveMenuDocument(ctx, f.owner, f.brand, f.branch, model.MenuEnglish, published.Version, empty, make([]byte, 32)); !errors.As(err, &pgErr) || pgErr.ConstraintName != "branch_menu_documents_page_count_check" {
		t.Fatalf("zero-page document must fail DB check: %v", err)
	}
}

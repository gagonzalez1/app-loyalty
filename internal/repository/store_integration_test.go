package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func TestPostgresCustomerStores(t *testing.T) {
	p := referralBillingPool(t)
	ctx := t.Context()
	repo := repository.New(p)
	var customer, other, staff, brand, branch, noCoords int64
	for _, u := range []struct {
		id          *int64
		email, kind string
	}{
		{&customer, "customer@map.test", "CLIENTE_FINAL"}, {&other, "other@map.test", "CLIENTE_FINAL"}, {&staff, "staff@map.test", "PERSONAL_MARCA"},
	} {
		if err := p.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash) VALUES($1::text,'Map',$2,CASE WHEN $2='CLIENTE_FINAL' THEN convert_to($1::text,'UTF8') ELSE NULL END) RETURNING id`, u.email, u.kind).Scan(u.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, `INSERT INTO marcas(nombre,descripcion) VALUES('Café 100%_\','Descripción real') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,direccion,localidad,provincia,latitud,longitud) VALUES($1,'Centro','Av. Literal 42','Quilmes','Buenos Aires',-34.7,-58.2) RETURNING id`, brand).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,latitud) VALUES($1,'Sin ubicación completa',-34) RETURNING id`, brand).Scan(&noCoords); err != nil {
		t.Fatal(err)
	}
	// Discovery is possible before a card, active program or paid subscription exists.
	items, err := repo.ListCustomerStoreLocations(ctx, customer, "")
	if err != nil || len(items) != 2 {
		t.Fatalf("discovery=%+v err=%v", items, err)
	}
	for _, item := range items {
		if item.BranchID == noCoords && (item.Latitude != nil || item.Longitude != nil) {
			t.Fatal("incomplete coordinates leaked")
		}
		if item.Favorite || item.Favorites != 0 || len(item.Benefits) != 0 {
			t.Fatalf("unexpected discovery data=%+v", item)
		}
	}
	for _, search := range []string{"100%_\\", "Quilmes", "Av. Literal", "Centro"} {
		got, e := repo.ListCustomerStoreLocations(ctx, customer, search)
		if e != nil || len(got) == 0 {
			t.Fatalf("search=%q results=%+v err=%v", search, got, e)
		}
	}
	if _, err = p.Exec(ctx, `INSERT INTO marcas(nombre) VALUES('Café 100XX'); INSERT INTO sucursales(marca_id,nombre) SELECT id,'Other' FROM marcas WHERE nombre='Café 100XX'`); err != nil {
		t.Fatal(err)
	}
	literal, err := repo.ListCustomerStoreLocations(ctx, customer, "100%_")
	if err != nil || len(literal) != 2 {
		t.Fatalf("literal search=%+v err=%v", literal, err)
	}
	// Empty search is a complete array, including results beyond the old 200 limit.
	if _, err = p.Exec(ctx, `INSERT INTO sucursales(marca_id,nombre) SELECT $1,'Branch '||n FROM generate_series(1,205) n`, brand); err != nil {
		t.Fatal(err)
	}
	items, err = repo.ListCustomerStoreLocations(ctx, customer, "")
	if err != nil || len(items) != 208 {
		t.Fatalf("complete collection=%d err=%v", len(items), err)
	}
	for i := 0; i < 2; i++ {
		state, e := repo.SetCustomerStoreFavorite(ctx, customer, branch, true)
		if e != nil || !state.Favorite || state.Favorites != 1 {
			t.Fatalf("PUT=%+v err=%v", state, e)
		}
	}
	state, err := repo.SetCustomerStoreFavorite(ctx, other, branch, true)
	if err != nil || state.Favorites != 2 {
		t.Fatalf("other PUT=%+v err=%v", state, err)
	}
	// Both users can favorite the same branch; repeated concurrent writes count once.
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := repo.SetCustomerStoreFavorite(ctx, customer, branch, true)
			if e == nil && (!s.Favorite || s.Favorites != 2) {
				e = fmt.Errorf("state=%+v", s)
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 2; i++ {
		state, err = repo.SetCustomerStoreFavorite(ctx, customer, branch, false)
		if err != nil || state.Favorite || state.Favorites != 1 {
			t.Fatalf("DELETE=%+v err=%v", state, err)
		}
	}
	// Concurrent opposite operations linearize on the branch lock; the last
	// explicit PUT establishes a deterministic final state after all complete.
	mixedErrors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(favorite bool) {
			defer wg.Done()
			_, e := repo.SetCustomerStoreFavorite(ctx, customer, branch, favorite)
			mixedErrors <- e
		}(i%2 == 0)
	}
	wg.Wait()
	close(mixedErrors)
	for e := range mixedErrors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, err = repo.SetCustomerStoreFavorite(ctx, customer, branch, false); err != nil {
		t.Fatal(err)
	}
	items, err = repository.New(p).ListCustomerStoreLocations(ctx, other, "Centro")
	if err != nil || len(items) != 1 || !items[0].Favorite || items[0].Favorites != 1 {
		t.Fatalf("persisted other=%+v err=%v", items, err)
	}
	if _, err = repo.SetCustomerStoreFavorite(ctx, staff, branch, true); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("staff=%v", err)
	}
	if _, err = repo.ListCustomerStoreLocations(ctx, staff, ""); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("staff list=%v", err)
	}
	for _, change := range []struct {
		sql string
		id  int64
	}{
		{`UPDATE sucursales SET activo=false WHERE id=$1`, branch},
		{`UPDATE sucursales SET activo=true,deleted_at=now() WHERE id=$1`, branch},
		{`UPDATE marcas SET activo=false WHERE id=$1`, brand},
		{`UPDATE marcas SET activo=true,deleted_at=now() WHERE id=$1`, brand},
	} {
		if _, err = p.Exec(ctx, change.sql, change.id); err != nil {
			t.Fatal(err)
		}
		if _, err = repo.SetCustomerStoreFavorite(ctx, customer, branch, true); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("hidden favorite=%v", err)
		}
		got, e := repo.ListCustomerStoreLocations(ctx, customer, "Centro")
		if e != nil || len(got) != 0 {
			t.Fatalf("hidden list=%+v err=%v", got, e)
		}
		if _, err = p.Exec(ctx, `UPDATE sucursales SET activo=true,deleted_at=NULL WHERE id=$1`, branch); err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(ctx, `UPDATE marcas SET activo=true,deleted_at=NULL WHERE id=$1`, brand); err != nil {
			t.Fatal(err)
		}
	}
	var program int64
	if err = p.QueryRow(ctx, `INSERT INTO programas_fidelidad(marca_id,tipo,sellos_por_acumulacion,nombre_unidad) VALUES($1,'SELLOS',1,'Sello') RETURNING id`, brand).Scan(&program); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO beneficios(programa_id,nombre,descripcion,requisito_sellos,activo,deleted_at,expires_on) VALUES($1,'Real','Texto real',3,true,NULL,NULL),($1,'Inactivo','',3,false,NULL,NULL),($1,'Borrado','',3,true,now(),NULL),($1,'Vencido','',3,true,NULL,'2000-01-01')`, program); err != nil {
		t.Fatal(err)
	}
	items, err = repo.ListCustomerStoreLocations(ctx, customer, "Centro")
	if err != nil || len(items) != 1 || len(items[0].Benefits) != 1 || items[0].Benefits[0].Name != "Real" || items[0].Benefits[0].RequiredStamps == nil || *items[0].Benefits[0].RequiredStamps != 3 || items[0].Benefits[0].RequiredPoints != nil {
		t.Fatalf("benefits=%+v err=%v", items, err)
	}
	// HTTP envelope and optional parameters match the frontend contract.
	h := &handler.Handler{Service: &service.Service{Repo: repo}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ActorKey, middleware.Actor{ID: customer, AccountType: "CLIENTE_FINAL"})
	})
	r.GET("/tiendas", h.CustomerStoreLocations)
	r.PUT("/tiendas/:branch_id/favorito", h.SetCustomerStoreFavorite)
	r.DELETE("/tiendas/:branch_id/favorito", h.DeleteCustomerStoreFavorite)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tiendas?search="+url.QueryEscape("  Centro  "), nil))
	var envelope web.Envelope[[]model.StoreLocation]
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || len(envelope.Data) != 1 {
		t.Fatalf("http=%d %s err=%v", w.Code, w.Body.String(), err)
	}
	for _, forbidden := range []string{"qr_hash", "saldo_sellos", "email", "usuario_id", "object_key"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("personal data: %s", w.Body.String())
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodPut, http.MethodDelete, http.MethodDelete} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, fmt.Sprintf("/tiendas/%d/favorito", branch), nil))
		if w.Code != 200 {
			t.Fatalf("mutation=%d %s", w.Code, w.Body.String())
		}
	}
	if _, err = repo.SetCustomerStoreFavorite(ctx, customer, 999999, true); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	export, err := repo.ExportAccount(ctx, other)
	if err != nil || len(export.FavoriteBranchIDs) != 1 || export.FavoriteBranchIDs[0] != branch {
		t.Fatalf("export favorites=%v err=%v", export.FavoriteBranchIDs, err)
	}
	if _, err = repo.AnonymizeAccount(ctx, other, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM sucursales_favoritas WHERE usuario_id=$1`, other).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted favorites=%d err=%v", count, err)
	}
	if _, err = repo.SetCustomerStoreFavorite(ctx, other, branch, true); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted actor=%v", err)
	}
}

// Verify the same preserved 0036–0040 SQL expands an existing 0035 database,
// preserves old values and accepts a second migration pass without duplication.
func TestPostgresStoreMigrationUpgrade(t *testing.T) {
	p := referralBillingPool(t)
	ctx := t.Context()
	rollbackCodeMigrationFixture(t, p)
	for _, name := range []string{"0042_branch_pdf_30mib", "0041_branch_pdf_menus", "0040_store_favorites", "0039_card_templates", "0038_benefit_expiration", "0037_movement_reversals", "0036_web_push"} {
		body, err := os.ReadFile("../../migrations/" + name + ".down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(ctx, string(body)); err != nil {
			t.Fatalf("prepare0035 %s: %v", name, err)
		}
		if _, err = p.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, name[:4]); err != nil {
			t.Fatal(err)
		}
	}
	repo := repository.New(p)
	if err := repo.CheckSchema(ctx, "0035"); err != nil {
		t.Fatal(err)
	}
	var brand int64
	if err := p.QueryRow(ctx, `INSERT INTO marcas(nombre,plantilla_tarjeta) VALUES('Preserved','COMIC_HQ') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		for _, name := range []string{"0036_web_push", "0037_movement_reversals", "0038_benefit_expiration", "0039_card_templates", "0040_store_favorites"} {
			var applied bool
			if err := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name[:4]).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied {
				continue
			}
			body, err := os.ReadFile("../../migrations/" + name + ".up.sql")
			if err != nil {
				t.Fatal(err)
			}
			tx, err := p.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, string(body)); err != nil {
				tx.Rollback(ctx)
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, name[:4]); err != nil {
				tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := repo.CheckSchema(ctx, "0040"); err != nil {
		t.Fatal(err)
	}
	var template string
	if err := p.QueryRow(ctx, `SELECT plantilla_tarjeta FROM marcas WHERE id=$1`, brand).Scan(&template); err != nil || template != "COMIC_HQ" {
		t.Fatalf("template=%q err=%v", template, err)
	}
}

// A merchant transaction holding the brand must still be able to edit its
// branch while a favorite waits. Taking the branch first creates a lock cycle.
func TestPostgresStoreFavoriteDoesNotBlockMerchantBranchEdit(t *testing.T) {
	p := referralBillingPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var customer, brand, branch int64
	if err := p.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash) VALUES('locks@map.test','Lock','CLIENTE_FINAL',decode('aabb','hex')) RETURNING id`).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES('Lock order') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre) VALUES($1,'Before edit') RETURNING id`, brand).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	merchant, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer merchant.Rollback(context.Background())
	var merchantPID int
	if err = merchant.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&merchantPID); err != nil {
		t.Fatal(err)
	}
	if _, err = merchant.Exec(ctx, `SELECT id FROM marcas WHERE id=$1 FOR UPDATE`, brand); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := repository.New(p).SetCustomerStoreFavorite(ctx, customer, branch, true); done <- e }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, merchantPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("favorite completed before merchant released brand: %v", e)
		case <-ctx.Done():
			t.Fatal("favorite never reached the held brand lock")
		case <-ticker.C:
		}
	}
	// This UPDATE would block if the waiting favorite already held the branch.
	if _, err = merchant.Exec(ctx, `SET LOCAL lock_timeout='300ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err = merchant.Exec(ctx, `UPDATE sucursales SET nombre='After edit',version=version+1 WHERE id=$1`, branch); err != nil {
		t.Fatalf("merchant edit blocked by favorite: %v", err)
	}
	if err = merchant.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("favorite did not complete after merchant commit")
	}
	locations, err := repository.New(p).ListCustomerStoreLocations(ctx, customer, "After edit")
	if err != nil || len(locations) != 1 || !locations[0].Favorite || locations[0].Favorites != 1 {
		t.Fatalf("updated branch=%+v err=%v", locations, err)
	}
}

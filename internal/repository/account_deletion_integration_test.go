package repository_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type accountCancellationProvider struct {
	mu                   sync.Mutex
	id, external, status string
	err                  error
	keys                 []string
}

func (*accountCancellationProvider) CreateSubscription(context.Context, model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{}, errors.New("unexpected checkout")
}
func (*accountCancellationProvider) GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{}, errors.New("unexpected lookup")
}
func (p *accountCancellationProvider) CancelSubscription(_ context.Context, id, key string) (model.BillingSubscriptionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, key)
	if id != p.id {
		return model.BillingSubscriptionResult{}, errors.New("wrong subscription")
	}
	return model.BillingSubscriptionResult{ID: p.id, ExternalReference: p.external, Status: p.status}, p.err
}

type deletionFixture struct {
	owner, brand, branch, customer, card int64
	provider                             *accountCancellationProvider
}

func accountDeletionFixture(t *testing.T, pool *pgxpool.Pool, subscriptionStatus string) deletionFixture {
	t.Helper()
	ctx := t.Context()
	var f deletionFixture
	if err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta,email_verified_at) VALUES($1,'hash','Owner','PERSONAL_MARCA',now()) RETURNING id`, uuid.NewString()+"@example.test").Scan(&f.owner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash) VALUES($1,'Customer','CLIENTE_FINAL',$2) RETURNING id`, uuid.NewString()+"@example.test", []byte(uuid.NewString())).Scan(&f.customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES('Closing brand') RETURNING id`).Scan(&f.brand); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,principal) VALUES($1,'Principal',true) RETURNING id`, f.brand).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO')`, []any{f.owner, f.brand}},
		{`INSERT INTO membresias_sucursales(membresia_id,sucursal_id,marca_id) SELECT id,$2,$3 FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$3`, []any{f.owner, f.branch, f.brand}},
		{`INSERT INTO programas_fidelidad(marca_id,tipo,sellos_por_acumulacion,nombre_unidad) VALUES($1,'SELLOS',1,'Sello')`, []any{f.brand}},
		{`INSERT INTO beneficios(programa_id,nombre,requisito_sellos) SELECT id,'Reward',5 FROM programas_fidelidad WHERE marca_id=$1`, []any{f.brand}},
		{`INSERT INTO accesos_demo(marca_id,tipo,precio_minor,moneda,cobro_automatico) VALUES($1,'SELLOS_FREE_TRIAL',0,'ARS',false)`, []any{f.brand}},
		{`INSERT INTO referral_campaigns(name,program_type,discount_bps,discount_charges,reward_bps,reward_charges,starts_at,ends_at) VALUES($1,'SELLOS',5000,3,2000,12,now()-interval '1 day',now()+interval '1 month')`, []any{fmt.Sprintf("Closing campaign %d", f.brand)}},
		{`INSERT INTO referral_codes(code,campaign_id,source_kind,source_brand_id) SELECT $1,id,'MERCHANT',$2 FROM referral_campaigns WHERE name=$3`, []any{fmt.Sprintf("CLOSE-%d", f.brand), f.brand, fmt.Sprintf("Closing campaign %d", f.brand)}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO tarjetas(usuario_id,marca_id,saldo_sellos) VALUES($1,$2,1) RETURNING id`, f.customer, f.brand).Scan(&f.card); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO historial_movimientos(operation_id,tarjeta_id,marca_id,sucursal_id,usuario_operador_id,operacion,programa_tipo,sentido,cantidad,saldo_anterior,saldo_posterior,marca_nombre_snapshot,sucursal_nombre_snapshot,programa_id_snapshot) SELECT $1,$2,$3,$4,$5,'ACUMULACION','SELLOS','CREDITO',1,0,1,'Closing brand','Principal',id FROM programas_fidelidad WHERE marca_id=$3`, uuid.NewString(), f.card, f.brand, f.branch, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO previews_movimiento(id,actor_id,sucursal_id,marca_id,cliente_id,tarjeta_id,operacion,programa_tipo,qr_hash,saldo_anterior,cantidad,saldo_posterior,request_fingerprint,expires_at) VALUES($1,$2,$3,$4,$5,$6,'ACUMULACION','SELLOS',decode('01','hex'),1,1,2,decode(repeat('02',32),'hex'),now()+interval '5 minutes')`, uuid.NewString(), f.owner, f.branch, f.brand, f.customer, f.card); err != nil {
		t.Fatal(err)
	}
	f.provider = &accountCancellationProvider{id: fmt.Sprintf("close-%d", f.brand), external: fmt.Sprintf("puntazo:brand:%d:%s", f.brand, uuid.NewString()), status: "canceled"}
	if subscriptionStatus != "" {
		var providerID any = f.provider.id
		if subscriptionStatus == "CREATING" {
			providerID = nil
		}
		if _, err := pool.Exec(ctx, `INSERT INTO suscripciones_marca(marca_id,proveedor,proveedor_suscripcion_id,referencia_externa,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor) VALUES($1,'MERCADO_PAGO',$2,$3,$4,'ARS',15000,1,15000)`, f.brand, providerID, f.provider.external, subscriptionStatus); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func assertDeletionState(t *testing.T, pool *pgxpool.Pool, f deletionFixture, deleted, closed bool) {
	t.Helper()
	var accountDeleted, brandClosed bool
	var operable, ledger int
	if err := pool.QueryRow(t.Context(), `SELECT NOT activo AND deleted_at IS NOT NULL AND password_hash IS NULL AND email::text=$2 FROM usuarios WHERE id=$1`, f.owner, fmt.Sprintf("deleted-%d@anon.invalid", f.owner)).Scan(&accountDeleted); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT NOT activo AND deleted_at IS NOT NULL FROM marcas WHERE id=$1`, f.brand).Scan(&brandClosed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM sucursales WHERE marca_id=$1 AND activo)+(SELECT count(*) FROM programas_fidelidad WHERE marca_id=$1 AND activo)+(SELECT count(*) FROM beneficios b JOIN programas_fidelidad p ON p.id=b.programa_id WHERE p.marca_id=$1 AND b.activo)+(SELECT count(*) FROM tarjetas WHERE marca_id=$1 AND activo)+(SELECT count(*) FROM accesos_demo WHERE marca_id=$1 AND activo)+(SELECT count(*) FROM referral_codes WHERE source_brand_id=$1 AND active)+(SELECT count(*) FROM previews_movimiento WHERE marca_id=$1 AND consumed_at IS NULL AND expires_at>now())`, f.brand).Scan(&operable); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM historial_movimientos WHERE marca_id=$1`, f.brand).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	wantOperable := 7
	if deleted {
		wantOperable--
	} // The departing actor's preview is always revoked.
	if closed {
		wantOperable = 0
	}
	if accountDeleted != deleted || brandClosed != closed || ledger != 1 || operable != wantOperable {
		t.Fatalf("account deleted=%t brand closed=%t operable=%d ledger=%d", accountDeleted, brandClosed, operable, ledger)
	}
	if closed {
		var activeMemberships int
		if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM membresias_marca WHERE marca_id=$1 AND activo)+(SELECT count(*) FROM membresias_sucursales WHERE marca_id=$1 AND activo)`, f.brand).Scan(&activeMemberships); err != nil || activeMemberships != 0 {
			t.Fatalf("active memberships=%d err=%v", activeMemberships, err)
		}
	}
}

func TestPostgresAccountDeletionLateSubscriptionResponse(t *testing.T) {
	pool := referralBillingPool(t)
	f := accountDeletionFixture(t, pool, "AUTHORIZED")
	repo := repository.New(pool)
	svc := service.New(repo, nil, config.Config{})
	svc.Billing = f.provider
	if _, err := svc.AnonymizeCurrentUser(t.Context(), f.owner, 1, model.AnonymizeAccountRequest{Confirmation: "BAJA"}); err != nil {
		t.Fatal(err)
	}
	next := svc.Now().Add(24 * time.Hour)
	stale := model.BillingSubscriptionResult{ID: f.provider.id, ExternalReference: f.provider.external, Status: "authorized", CheckoutURL: "https://provider.example.test/checkout", NextPaymentDate: &next}
	if err := repo.RecordSubscriptionWebhook(t.Context(), uuid.NewString(), "subscription_preapproval", stale); err != nil {
		t.Fatal(err)
	}
	record, err := repo.GetSubscriptionRecord(t.Context(), f.brand)
	if err != nil || record.Subscription.Status != "CANCELLED" || record.Subscription.CheckoutURL != "" || record.Subscription.NextPaymentDate != nil {
		t.Fatalf("late webhook=%+v err=%v", record, err)
	}
	out, err := repo.UpdateSubscriptionFromProvider(t.Context(), stale)
	if err != nil || out.Status != "CANCELLED" || out.CheckoutURL != "" || out.NextPaymentDate != nil {
		t.Fatalf("late provider response=%+v err=%v", out, err)
	}
	assertDeletionState(t, pool, f, true, true)
}

func TestPostgresAccountDeletionPreconditions(t *testing.T) {
	pool := referralBillingPool(t)
	f := accountDeletionFixture(t, pool, "AUTHORIZED")
	svc := service.New(repository.New(pool), nil, config.Config{})
	svc.Billing = f.provider
	for _, tc := range []struct {
		version      int
		confirmation string
		want         error
	}{
		{1, "BORRAR", service.ErrInvalidRequest},
		{0, "BAJA", service.ErrInvalidRequest},
		{2, "BAJA", repository.ErrPreconditionFailed},
	} {
		if _, err := svc.AnonymizeCurrentUser(t.Context(), f.owner, tc.version, model.AnonymizeAccountRequest{Confirmation: tc.confirmation}); !errors.Is(err, tc.want) {
			t.Fatalf("version=%d confirmation=%s err=%v", tc.version, tc.confirmation, err)
		}
		assertDeletionState(t, pool, f, false, false)
	}
	if len(f.provider.keys) != 0 {
		t.Fatal("provider called before validating account preconditions")
	}
}

func TestPostgresAccountDeletion(t *testing.T) {
	pool := referralBillingPool(t)
	repo := repository.New(pool)
	for _, tc := range []struct {
		name, status           string
		billing                bool
		providerStatus         string
		providerErr, errorWant error
		calls                  int
		deleted                bool
		providerCallStarted    bool
	}{
		{name: "last owner without subscription", deleted: true},
		{name: "last owner canceled subscription", status: "CANCELLED", deleted: true},
		{name: "last owner recurring subscription", status: "AUTHORIZED", billing: true, providerStatus: "canceled", calls: 1, deleted: true},
		{name: "provider failure rolls back", status: "AUTHORIZED", billing: true, providerErr: errors.New("provider unavailable"), errorWant: service.ErrBillingProviderFailure, calls: 1},
		{name: "provider fails to confirm cancellation", status: "AUTHORIZED", billing: true, providerStatus: "authorized", errorWant: service.ErrBillingProviderFailure, calls: 1},
		{name: "billing disabled rolls back", status: "AUTHORIZED", errorWant: service.ErrBillingUnavailable},
		{name: "abandoned checkout closes locally", status: "CREATING", deleted: true},
		{name: "provider checkout in progress rolls back", status: "CREATING", billing: true, providerCallStarted: true, errorWant: service.ErrBillingInProgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := accountDeletionFixture(t, pool, tc.status)
			if tc.providerCallStarted {
				if _, err := pool.Exec(t.Context(), `UPDATE suscripciones_marca SET provider_call_started_at=now() WHERE marca_id=$1`, f.brand); err != nil {
					t.Fatal(err)
				}
			}
			f.provider.status = tc.providerStatus
			f.provider.err = tc.providerErr
			svc := service.New(repo, nil, config.Config{})
			if tc.billing {
				svc.Billing = f.provider
			}
			out, err := svc.AnonymizeCurrentUser(t.Context(), f.owner, 1, model.AnonymizeAccountRequest{Confirmation: "BAJA"})
			if !errors.Is(err, tc.errorWant) {
				t.Fatalf("deletion err=%v want=%v", err, tc.errorWant)
			}
			if tc.deleted && (!out.AccessRevoked || out.LedgerPreserved || out.Status != "COMPLETADA") {
				t.Fatalf("deletion=%+v", out)
			}
			if len(f.provider.keys) != tc.calls {
				t.Fatalf("provider calls=%d want=%d", len(f.provider.keys), tc.calls)
			}
			assertDeletionState(t, pool, f, tc.deleted, tc.deleted)
			if tc.status != "" {
				var status string
				if err := pool.QueryRow(t.Context(), `SELECT estado FROM suscripciones_marca WHERE marca_id=$1`, f.brand).Scan(&status); err != nil {
					t.Fatal(err)
				}
				want := tc.status
				if tc.deleted {
					want = "CANCELLED"
				}
				if status != want {
					t.Fatalf("subscription status=%s want=%s", status, want)
				}
			}
		})
	}
}

func TestPostgresAccountDeletionSharedOwners(t *testing.T) {
	pool := referralBillingPool(t)
	for _, tc := range []struct {
		name                   string
		active, deleted, close bool
	}{
		{"active co-owner", true, false, false}, {"inactive co-owner", false, false, true}, {"deleted co-owner", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := accountDeletionFixture(t, pool, "AUTHORIZED")
			var coOwner int64
			if err := pool.QueryRow(t.Context(), `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta,activo,deleted_at) VALUES($1,'hash','Co-owner','PERSONAL_MARCA',$2,CASE WHEN $3 THEN now() ELSE NULL END) RETURNING id`, uuid.NewString()+"@example.test", tc.active, tc.deleted).Scan(&coOwner); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO')`, coOwner, f.brand); err != nil {
				t.Fatal(err)
			}
			svc := service.New(repository.New(pool), nil, config.Config{})
			svc.Billing = f.provider
			if _, err := svc.AnonymizeCurrentUser(t.Context(), f.owner, 1, model.AnonymizeAccountRequest{Confirmation: "BAJA"}); err != nil {
				t.Fatal(err)
			}
			assertDeletionState(t, pool, f, true, tc.close)
			wantCalls := 0
			if tc.close {
				wantCalls = 1
			}
			if len(f.provider.keys) != wantCalls {
				t.Fatalf("provider calls=%d", len(f.provider.keys))
			}
			var status string
			if err := pool.QueryRow(t.Context(), `SELECT estado FROM suscripciones_marca WHERE marca_id=$1`, f.brand).Scan(&status); err != nil {
				t.Fatal(err)
			}
			wantStatus := "AUTHORIZED"
			if tc.close {
				wantStatus = "CANCELLED"
			}
			if status != wantStatus {
				t.Fatalf("subscription=%s", status)
			}
		})
	}
}

func TestPostgresAccountDeletionConcurrentOwners(t *testing.T) {
	pool := referralBillingPool(t)
	f := accountDeletionFixture(t, pool, "AUTHORIZED")
	var coOwner int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta) VALUES($1,'hash','Co-owner','PERSONAL_MARCA') RETURNING id`, uuid.NewString()+"@example.test").Scan(&coOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO')`, coOwner, f.brand); err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(pool), nil, config.Config{})
	svc.Billing = f.provider
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []int64{f.owner, coOwner} {
		go func(id int64) {
			<-start
			_, err := svc.AnonymizeCurrentUser(t.Context(), id, 1, model.AnonymizeAccountRequest{Confirmation: "BAJA"})
			results <- err
		}(id)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertDeletionState(t, pool, f, true, true)
	if len(f.provider.keys) < 1 {
		t.Fatal("subscription was not canceled")
	}
	for _, key := range f.provider.keys {
		if key != f.provider.keys[0] {
			t.Fatal("unstable cancellation retry key")
		}
	}
}

package repository_test

import (
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"context"
	"errors"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func TestPostgresBranchProrationDurableSimulator(t *testing.T) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	repo := repository.New(pool)
	var user, brand int64
	if e := pool.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta) VALUES('branch@example.test','hash','Owner','PERSONAL_MARCA') RETURNING id`).Scan(&user); e != nil {
		t.Fatal(e)
	}
	if e := pool.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES('Branch brand') RETURNING id`).Scan(&brand); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO')`, `INSERT INTO sucursales(marca_id,nombre) SELECT $2,'Principal' WHERE $1::bigint>0`, `INSERT INTO programas_fidelidad(marca_id,tipo,sellos_por_acumulacion,nombre_unidad) SELECT $2,'SELLOS',1,'Sello' WHERE $1::bigint>0`} {
		if _, e := pool.Exec(ctx, q, user, brand); e != nil {
			t.Fatal(e)
		}
	}
	now := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	next := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	if _, e := pool.Exec(ctx, `INSERT INTO suscripciones_marca(marca_id,proveedor,proveedor_suscripcion_id,referencia_externa,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,full_unit_price_minor,proximo_cobro_at,created_at,trial_months,trial_ends_at) VALUES($1,'MERCADO_PAGO','sim-subscription','puntazo:brand:branchtest','AUTHORIZED','ARS',2500000,1,2500000,2500000,$2,'2026-09-01T15:00:00Z',1,'2026-10-01T15:00:00Z')`, brand, next); e != nil {
		t.Fatal(e)
	}
	svc := service.Service{Repo: repo, Now: func() time.Time { return now }, Config: config.Config{BranchProrationEnabled: true, BranchPaymentSimulator: true, PublicAppURL: "http://localhost:4175", MercadoPagoBranchPrice: 2500000}}
	quote := func(name string) model.BranchQuote {
		q, e := svc.QuoteBranch(ctx, user, brand, model.CreateBranchRequest{Name: name})
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	assertCount := func(want int64) {
		var got int64
		if e := pool.QueryRow(ctx, `SELECT count(*) FROM sucursales WHERE marca_id=$1 AND activo`, brand).Scan(&got); e != nil || got != want {
			t.Fatalf("branches=%d want%d err%v", got, want, e)
		}
	}
	q := quote("Second")
	if q.ProrationAmountCents != 1370968 || q.NewMonthlyAmountCents != 5000000 {
		t.Fatalf("quote%+v", q)
	}
	key := uuid.NewString()
	var wg sync.WaitGroup
	ops := make([]model.BranchOperation, 4)
	errs := make([]error, 4)
	for i := range ops {
		wg.Add(1)
		go func(i int) { defer wg.Done(); ops[i], errs[i] = svc.ConfirmBranch(ctx, user, brand, q.QuoteID, key) }(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil || ops[i].ID != ops[0].ID {
			t.Fatalf("duplicateconfirm %d %v %+v", i, e, ops[i])
		}
	}
	op := ops[0]
	assertCount(1)
	qOther := quote("Other")
	if _, e := svc.ConfirmBranch(ctx, user, brand, qOther.QuoteID, uuid.NewString()); !errors.Is(e, repository.ErrIdempotencyInProgress) {
		t.Fatalf("concurrentbrand=%v", e)
	}
	op, e := svc.SimulateBranch(ctx, user, brand, op.ID, "update_fail")
	if e != nil || op.Status != "PLAN_UPDATING" {
		t.Fatalf("updatefail %+v %v", op, e)
	}
	assertCount(1)
	// Recovery after quote's 10min deadline must not charge again or expire payment.
	now = now.Add(20 * time.Minute)
	op, e = svc.SimulateBranch(ctx, user, brand, op.ID, "recover")
	if e != nil || op.Status != "COMPLETED" || op.Branch == nil {
		t.Fatalf("recover %+v %v", op, e)
	}
	assertCount(2)
	replay, e := svc.ConfirmBranch(ctx, user, brand, q.QuoteID, key)
	if e != nil || replay.ID != op.ID {
		t.Fatalf("idempotentcompleted %+v %v", replay, e)
	}
	assertCount(2)
	var total, count int64
	var savedNext time.Time
	var charges int
	if e = pool.QueryRow(ctx, `SELECT importe_mensual_minor,cantidad_sucursales,proximo_cobro_at FROM suscripciones_marca WHERE marca_id=$1`, brand).Scan(&total, &count, &savedNext); e != nil || total != 5000000 || count != 2 || !savedNext.Equal(next) {
		t.Fatalf("subscription %d %d %v err%v", total, count, savedNext, e)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM referral_charges WHERE brand_id=$1`, brand).Scan(&charges); e != nil || charges != 0 {
		t.Fatal("oneoff consumed referral charge", e, charges)
	}
	// A permanently failed update is compensated, and repeated events remain inert.
	q = quote("Third")
	op, e = svc.ConfirmBranch(ctx, user, brand, q.QuoteID, uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	op, e = svc.SimulateBranch(ctx, user, brand, op.ID, "refund_fail")
	if e != nil || op.Status != "REFUND_PENDING" {
		t.Fatalf("refundfail %+v %v", op, e)
	}
	assertCount(2)
	op, e = svc.SimulateBranch(ctx, user, brand, op.ID, "recover")
	if e != nil || op.Status != "REFUNDED" {
		t.Fatalf("refundrecover %+v %v", op, e)
	}
	assertCount(2)
	// Expired quote and changed price rejected before any billing operation.
	q = quote("Expired")
	now = now.Add(11 * time.Minute)
	if _, e = svc.ConfirmBranch(ctx, user, brand, q.QuoteID, uuid.NewString()); !errors.Is(e, repository.ErrQuoteExpired) {
		t.Fatalf("expiry %v", e)
	}
	q = quote("Changed")
	if _, e = pool.Exec(ctx, `UPDATE suscripciones_marca SET precio_sucursal_minor=2000000,importe_mensual_minor=4000000 WHERE marca_id=$1`, brand); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ConfirmBranch(ctx, user, brand, q.QuoteID, uuid.NewString()); !errors.Is(e, repository.ErrQuoteChanged) {
		t.Fatalf("changed %v", e)
	}
	// Backend owner permission is independent of UI visibility.
	if _, e = pool.Exec(ctx, `UPDATE membresias_marca SET rol='ADMINISTRADOR' WHERE usuario_id=$1`, user); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.QuoteBranch(ctx, user, brand, model.CreateBranchRequest{Name: "Forbidden"}); !errors.Is(e, repository.ErrForbidden) {
		t.Fatalf("permission %v", e)
	}
	// Simulator controls never exist as effective operations when disabled.
	svc.Config.BranchPaymentSimulator = false
	if _, e = svc.SimulateBranch(ctx, user, brand, op.ID, "approve"); !errors.Is(e, service.ErrForbidden) {
		t.Fatalf("simulator gate %v", e)
	}
	_ = context.Canceled
}

type branchFakeProvider struct {
	next              time.Time
	amount            int64
	reference         string
	payment           model.BillingPayment
	checkoutReference string
	puts, refunds     int
	duplicateChecks   int
	uncertain         bool
}

func (f *branchFakeProvider) CreateSubscription(context.Context, model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{}, errors.New("unused")
}
func (f *branchFakeProvider) CancelSubscription(context.Context, string, string) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{}, errors.New("unused")
}
func (f *branchFakeProvider) GetPayment(context.Context, string) (model.BillingPayment, error) {
	return f.payment, nil
}
func (f *branchFakeProvider) GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{ID: "fake-sub", Status: "authorized", ExternalReference: f.reference, AmountMinor: f.amount, NextPaymentDate: &f.next}, nil
}
func (f *branchFakeProvider) UpdateSubscriptionAmount(_ context.Context, _ string, amount int64, _ string) (model.BillingSubscriptionResult, error) {
	f.puts++
	f.amount = amount
	if f.uncertain {
		f.uncertain = false
		return model.BillingSubscriptionResult{}, errors.New("network lost after PUT")
	}
	return f.GetSubscription(context.Background(), "")
}
func (f *branchFakeProvider) CreateBranchCheckout(_ context.Context, in model.BranchPaymentRequest) (model.BranchPaymentCheckout, error) {
	f.checkoutReference = in.Reference
	return model.BranchPaymentCheckout{ID: "fakecheckout", URL: "https://checkout.example.test"}, nil
}
func (f *branchFakeProvider) FindBranchPayment(context.Context, string) (model.BillingPayment, bool, error) {
	return f.payment, f.payment.ID != "", nil
}
func (f *branchFakeProvider) ReconcileBranchDuplicatePayments(context.Context, string, string) error {
	f.duplicateChecks++
	return nil
}
func (f *branchFakeProvider) RefundBranchPayment(context.Context, string, string) error {
	f.refunds++
	f.payment.RefundedMinor = f.payment.AmountMinor
	f.payment.Status = "refunded"
	return nil
}
func TestPostgresBranchProviderUncertainUpdateCompensation(t *testing.T) {
	t.Run("paid", func(t *testing.T) { testBranchProviderCompensation(t, false, false) })
	t.Run("free trial", func(t *testing.T) { testBranchProviderCompensation(t, true, false) })
	t.Run("post-completion duplicates and repeated webhooks", func(t *testing.T) { testBranchProviderCompensation(t, false, true) })
}
func testBranchProviderCompensation(t *testing.T, free, complete bool) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	repo := repository.New(pool)
	var user, brand int64
	if e := pool.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta) VALUES('provider@example.test','hash','Owner','PERSONAL_MARCA') RETURNING id`).Scan(&user); e != nil {
		t.Fatal(e)
	}
	if e := pool.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES('Provider brand') RETURNING id`).Scan(&brand); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO')`, `INSERT INTO sucursales(marca_id,nombre) SELECT $2,'Principal' WHERE $1::bigint>0`, `INSERT INTO programas_fidelidad(marca_id,tipo,sellos_por_acumulacion,nombre_unidad) SELECT $2,'SELLOS',1,'Sello' WHERE $1::bigint>0`} {
		if _, e := pool.Exec(ctx, q, user, brand); e != nil {
			t.Fatal(e)
		}
	}
	now := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	next := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	if _, e := pool.Exec(ctx, `INSERT INTO suscripciones_marca(marca_id,proveedor,proveedor_suscripcion_id,referencia_externa,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,full_unit_price_minor,proximo_cobro_at,created_at,trial_months,trial_ends_at) VALUES($1,'MERCADO_PAGO','fake-sub','puntazo:brand:provider','AUTHORIZED','ARS',2500000,1,2500000,2500000,$2,'2026-09-01T15:00:00Z',1,'2026-10-01T15:00:00Z')`, brand, next); e != nil {
		t.Fatal(e)
	}
	if free {
		if _, e := pool.Exec(ctx, `UPDATE suscripciones_marca SET trial_ends_at=$2 WHERE marca_id=$1`, brand, next); e != nil {
			t.Fatal(e)
		}
	}
	fake := &branchFakeProvider{next: next, amount: 2500000, reference: "puntazo:brand:provider"}
	svc := service.Service{Repo: repo, Billing: fake, Now: func() time.Time { return now }, Config: config.Config{BranchProrationEnabled: true, PublicAppURL: "http://localhost:4175"}}
	q, e := svc.QuoteBranch(ctx, user, brand, model.CreateBranchRequest{Name: "Pendingbranch"})
	if e != nil {
		t.Fatal(e)
	}
	op, e := svc.ConfirmBranch(ctx, user, brand, q.QuoteID, uuid.NewString())
	wantStatus := "PAYMENT_PENDING"
	if free {
		wantStatus = "PLAN_UPDATING"
	}
	if e != nil || op.Status != wantStatus {
		t.Fatalf("checkout%+v %v", op, e)
	}
	fake.payment = model.BillingPayment{ID: "44", Status: "approved", Currency: "ARS", ExternalReference: fake.checkoutReference, AmountMinor: q.ProrationAmountCents, ApprovedAt: &now}
	if !free {
		if e = repo.ReconcileBranch(ctx, op.ID, fake, svc.Config.PublicAppURL, now); e != nil {
			t.Fatal(e)
		}
	} // durable provider PUT intent
	fake.uncertain = true
	if e = repo.ReconcileBranch(ctx, op.ID, fake, svc.Config.PublicAppURL, now); e != nil {
		t.Fatal(e)
	}
	if fake.amount != 5000000 || fake.puts != 1 {
		t.Fatalf("uncertain PUT not simulated %+v", fake)
	}
	if complete {
		// Verified webhook retries the uncertain provider update using readback.
		if e = svc.ApplySubscriptionWebhook(ctx, "event", "payment", "44"); e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 3; i++ {
			if e = svc.ApplySubscriptionWebhook(ctx, "event", "payment", "44"); e != nil {
				t.Fatal(e)
			}
		}
		var status string
		var branches int
		if e = pool.QueryRow(ctx, `SELECT o.status,(SELECT count(*) FROM sucursales WHERE marca_id=o.marca_id) FROM branch_operations o WHERE id=$1`, op.ID).Scan(&status, &branches); e != nil {
			t.Fatal(e)
		}
		if status != "COMPLETED" || branches != 2 || fake.puts != 1 || fake.duplicateChecks != 3 {
			t.Fatalf("repeatedwebhooks status%s branches%d puts%d duplicatechecks%d", status, branches, fake.puts, fake.duplicateChecks)
		}
		return
	}
	// Revocation prevents activation, but compensation cannot be prevented by UI auth.
	if _, e = pool.Exec(ctx, `UPDATE membresias_marca SET activo=false WHERE usuario_id=$1`, user); e != nil {
		t.Fatal(e)
	}
	now = now.Add(20 * time.Minute)
	if e = repo.ReconcileBranch(ctx, op.ID, fake, svc.Config.PublicAppURL, now); e != nil {
		t.Fatal(e)
	}
	var status string
	var count int
	var monthly int64
	if e = pool.QueryRow(ctx, `SELECT o.status,(SELECT count(*) FROM sucursales WHERE marca_id=o.marca_id),s.importe_mensual_minor FROM branch_operations o JOIN suscripciones_marca s ON s.marca_id=o.marca_id WHERE o.id=$1`, op.ID).Scan(&status, &count, &monthly); e != nil {
		t.Fatal(e)
	}
	wantStatus = "REFUNDED"
	wantRefunds := 1
	if free {
		wantStatus = "FAILED"
		wantRefunds = 0
	}
	if status != wantStatus || count != 1 || monthly != 2500000 || fake.amount != 2500000 || fake.refunds != wantRefunds {
		t.Fatalf("unsafe compensation status%s count%d monthly%d fake%+v", status, count, monthly, fake)
	}
	if e = repo.ReconcileBranch(ctx, op.ID, fake, svc.Config.PublicAppURL, now); e != nil || fake.refunds != wantRefunds {
		t.Fatal("repeated reconcile duplicatedrefund", e, fake.refunds)
	}
}

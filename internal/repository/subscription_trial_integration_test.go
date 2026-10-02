package repository_test

import (
	"clientesFrecuentes/internal/mercadopago"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recoveringBilling struct {
	createErr error
	result    model.BillingSubscriptionResult
	creates   int
	found     bool
	requested *time.Time
}

func (f *recoveringBilling) CreateSubscription(_ context.Context, in model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error) {
	f.creates++
	f.requested = in.StartDate
	if in.FreeTrialMonths != 0 {
		return model.BillingSubscriptionResult{}, fmt.Errorf("trial restarted")
	}
	return f.result, f.createErr
}
func (f *recoveringBilling) GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error) {
	return f.result, nil
}
func (f *recoveringBilling) CancelSubscription(context.Context, string, string) (model.BillingSubscriptionResult, error) {
	return f.result, nil
}
func (f *recoveringBilling) FindSubscription(context.Context, string) (model.BillingSubscriptionResult, bool, error) {
	return f.result, f.found, nil
}

func TestPostgresFirstLoginTrialAndCheckoutRecovery(t *testing.T) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	repo := repository.New(pool)
	var user, brand int64
	if err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta) VALUES('trial@example.test','hash','Owner','PERSONAL_MARCA') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES('Trial brand') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO')`, `INSERT INTO sucursales(marca_id,nombre) SELECT $2,'Principal' WHERE $1::bigint>0`, `INSERT INTO programas_fidelidad(marca_id,tipo,sellos_por_acumulacion,nombre_unidad) SELECT $2,'SELLOS',1,'Sello' WHERE $1::bigint>0`} {
		// The second/third statements only need the brand bind.
		if _, err := pool.Exec(ctx, q, user, brand); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	if err := repo.CreateSession(ctx, uuid.NewString(), user, []byte("first"), start.AddDate(0, 0, 60), start); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, uuid.NewString(), user, []byte("second"), start.AddDate(0, 0, 90), start.AddDate(0, 0, 20)); err != nil {
		t.Fatal(err)
	}
	var saved time.Time
	if err := pool.QueryRow(ctx, `SELECT first_login_at FROM usuarios WHERE id=$1`, user).Scan(&saved); err != nil || !saved.Equal(start) {
		t.Fatalf("first login changed: %v %v", saved, err)
	}
	svc := service.Service{Repo: repo, Now: func() time.Time { return start.AddDate(0, 0, 7) }}
	repo.Now = svc.Now
	svc.Config.MercadoPagoBranchPrice = 1999900
	provider := &recoveringBilling{createErr: &mercadopago.RequestError{Status: 400}}
	svc.Billing = provider
	offer, err := svc.Subscription(ctx, user, brand)
	if err != nil || offer.TrialEndsAt == nil || !offer.TrialEndsAt.Equal(repository.TrialEnd(start)) {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
	if _, err = svc.CreateSubscriptionCheckout(ctx, user, brand, uuid.NewString()); !errors.Is(err, service.ErrBillingRejected) {
		t.Fatalf("rejection=%v", err)
	}
	record, err := repo.GetSubscriptionRecord(ctx, brand)
	if err != nil || record.Subscription.Status != "CANCELLED" {
		t.Fatalf("rejected checkout locked: %+v %v", record, err)
	}
	if provider.requested == nil || !provider.requested.Equal(*offer.TrialEndsAt) {
		t.Fatal("first charge didn't respect fixed deadline")
	}
	provider.createErr = errors.New("network timeout")
	if _, err = svc.CreateSubscriptionCheckout(ctx, user, brand, uuid.NewString()); !errors.Is(err, service.ErrBillingProviderFailure) {
		t.Fatal(err)
	}
	if _, err = svc.CreateSubscriptionCheckout(ctx, user, brand, uuid.NewString()); !errors.Is(err, service.ErrBillingInProgress) || provider.creates != 2 {
		t.Fatalf("uncertain operation replayed: %v calls=%d", err, provider.creates)
	}
	// Legacy uncertain reservations have no stored deadline. Read-only display
	// may derive the known first-login deadline without rewriting the reservation.
	if _, err = pool.Exec(ctx, `UPDATE suscripciones_marca SET trial_ends_at=NULL WHERE marca_id=$1`, brand); err != nil {
		t.Fatal(err)
	}
	legacy, err := svc.Subscription(ctx, user, brand)
	if err != nil || legacy.TrialEndsAt == nil || !legacy.TrialEndsAt.Equal(*offer.TrialEndsAt) {
		t.Fatalf("legacy notice=%+v err=%v", legacy, err)
	}
	record, _ = repo.GetSubscriptionRecord(ctx, brand)
	if record.Subscription.TrialEndsAt != nil {
		t.Fatal("display rewrote a legacy reservation")
	}
	provider.result = model.BillingSubscriptionResult{ID: "recovered", Status: "pending", ExternalReference: record.ExternalReference, CheckoutURL: "https://mp.test/checkout"}
	provider.found = true
	recovered, err := svc.Subscription(ctx, user, brand)
	if err != nil || recovered.Status != "PENDING" || recovered.CheckoutURL == "" || provider.creates != 2 {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	if !recovered.TrialEndsAt.Equal(*offer.TrialEndsAt) {
		t.Fatal("retry extended trial")
	}
	if _, err = pool.Exec(ctx, `UPDATE marcas SET trial_start_estimated=true WHERE id=$1`, brand); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CreateSubscriptionCheckout(ctx, user, brand, uuid.NewString()); !errors.Is(err, service.ErrTrialStartUnknown) || provider.creates != 2 {
		t.Fatalf("estimated deadline reached payment provider: %v", err)
	}
	if _, err = svc.Subscription(ctx, user+100, brand); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("non owner could inspect billing: %v", err)
	}
}

func TestPostgresTrialMigrationPreservesHistoricalDeadline(t *testing.T) {
	pool := referralBillingPool(t)
	ctx := t.Context()
	// Restore only this isolated fixture's pre-0032 shape, then test the upgrade.
	for _, q := range []string{`ALTER TABLE usuarios DROP COLUMN first_login_at,DROP COLUMN first_login_estimated`, `ALTER TABLE marcas DROP COLUMN trial_started_at,DROP COLUMN trial_start_estimated`, `ALTER TABLE suscripciones_marca DROP COLUMN trial_ends_at,DROP COLUMN checkout_rejected CASCADE`} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE suscripciones_marca ADD CONSTRAINT suscripciones_marca_provider_id_check CHECK ((estado='CREATING' AND proveedor_suscripcion_id IS NULL) OR (estado<>'CREATING' AND proveedor_suscripcion_id IS NOT NULL))`); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta,created_at) VALUES('legacy-trial@example.test','hash','Owner','PERSONAL_MARCA','2025-01-01T15:00:00Z') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0032_first_login_trial.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	var start time.Time
	var estimated bool
	if err = pool.QueryRow(ctx, `SELECT first_login_at,first_login_estimated FROM usuarios WHERE id=$1`, user).Scan(&start, &estimated); err != nil {
		t.Fatal(err)
	}
	if !estimated || start.Year() != 2025 {
		t.Fatalf("legacy account received a fresh trial: %v %v", start, estimated)
	}
	if err = repository.New(pool).CreateSession(ctx, uuid.NewString(), user, []byte("legacy"), time.Now().Add(time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	var unchanged time.Time
	if err = pool.QueryRow(ctx, `SELECT first_login_at FROM usuarios WHERE id=$1`, user).Scan(&unchanged); err != nil || !unchanged.Equal(start) {
		t.Fatal("legacy trial restarted")
	}
}

func TestPostgresAuthenticatedDemoRegistrationStartsTrial(t *testing.T) {
	pool := referralBillingPool(t)
	repo := repository.New(pool)
	// Session expiry must remain after the database-generated creation time.
	// PostgreSQL stores timestamps at microsecond precision.
	start := time.Now().UTC().Truncate(time.Microsecond)
	_, err := repo.CreateDemoMerchant(t.Context(), uuid.NewString(), make([]byte, 32), "demo-trial@example.test", "unused", "Owner", "Test", "Trial demo", "Principal", model.BranchRegistrationLocation{}, "SELLOS", "", uuid.NewString(), []byte("refresh"), start.Add(24*time.Hour), start, &start, nil, time.Time{}, nil, func(model.User, model.MerchantContext) ([]byte, error) { return []byte("{}"), nil })
	if err != nil {
		t.Fatal(err)
	}
	var first, brandStart time.Time
	var estimated bool
	if err = pool.QueryRow(t.Context(), `SELECT u.first_login_at,m.trial_started_at,m.trial_start_estimated FROM usuarios u JOIN membresias_marca mm ON mm.usuario_id=u.id JOIN marcas m ON m.id=mm.marca_id WHERE u.email='demo-trial@example.test'`).Scan(&first, &brandStart, &estimated); err != nil {
		t.Fatal(err)
	}
	if !first.Equal(start) || !brandStart.Equal(start) || estimated {
		t.Fatalf("first=%v brand=%v estimated=%t", first, brandStart, estimated)
	}
}

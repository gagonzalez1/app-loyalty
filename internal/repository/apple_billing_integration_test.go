package repository_test

import (
	"clientesFrecuentes/internal/applebilling"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func appleFixture(t *testing.T) (*pgxpool.Pool, *repository.Repository, deletionFixture) {
	t.Helper()
	p := referralBillingPool(t)
	f := accountDeletionFixture(t, p, "")
	if _, e := p.Exec(t.Context(), `UPDATE marcas SET created_at=now()-interval '40 days' WHERE id=$1`, f.brand); e != nil {
		t.Fatal(e)
	}
	repo := repository.New(p)
	repo.AppleBillingEnvironment = "Xcode"
	return p, repo, f
}
func appleTransaction(f deletionFixture, intent model.ApplePurchaseIntent, now time.Time) model.AppleTransaction {
	return model.AppleTransaction{TransactionID: uuid.NewString(), OriginalTransactionID: uuid.NewString(), AppAccountToken: intent.AppAccountToken, ProductID: intent.ProductID, BundleID: "com.puntazo.app", Environment: "Xcode", Type: "Auto-Renewable Subscription", OwnershipType: "PURCHASED", PurchaseDate: now.Add(-time.Minute).UnixMilli(), ExpiresDate: now.AddDate(0, 1, 0).UnixMilli(), SignedDate: now.UnixMilli(), Currency: "USD", Price: 4990}
}
func appleIntent(t *testing.T, r *repository.Repository, f deletionFixture, capacity int) model.ApplePurchaseIntent {
	t.Helper()
	i, e := r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, uuid.NewString(), model.ApplePurchaseIntentRequest{ProductID: fmt.Sprintf("com.puntazo.app.merchant.sellos.%d.monthly", capacity)}, "Xcode")
	if e != nil {
		t.Fatal(e)
	}
	return i
}
func TestPostgresAppleIntentBindingAndConcurrentCapacity(t *testing.T) {
	p, r, f := appleFixture(t)
	key := uuid.NewString()
	req := model.ApplePurchaseIntentRequest{ProductID: "com.puntazo.app.merchant.sellos.2.monthly"}
	i, e := r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, key, req, "Xcode")
	if e != nil {
		t.Fatal(e)
	}
	replay, e := r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, key, req, "Xcode")
	if e != nil || replay.IntentID != i.IntentID {
		t.Fatal("intent replay", e)
	}
	changed := req
	changed.ProductID = "com.puntazo.app.merchant.sellos.3.monthly"
	if _, e = r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, key, changed, "Xcode"); !errors.Is(e, repository.ErrIdempotencyConflict) {
		t.Fatal("idempotency fingerprint", e)
	}
	if _, e = r.CreateApplePurchaseIntent(t.Context(), f.customer, f.brand, uuid.NewString(), req, "Xcode"); !errors.Is(e, repository.ErrForbidden) {
		t.Fatal("nonowner purchase", e)
	}
	// Ask-to-Buy/StoreKit approval may remain pending beyond the intent TTL.
	// Reserving the provider at intent creation prevents a parallel web MP checkout.
	if _, _, e = r.ReserveSubscriptionCheckout(t.Context(), f.owner, f.brand, uuid.NewString(), 15000, 20000); !errors.Is(e, repository.ErrBillingProviderConflict) {
		t.Fatal("MP checkout raced pending Apple purchase", e)
	}
	freeState, e := r.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || freeState.Mode != "FREE" || freeState.Restriction != "" {
		t.Fatal("reservation removed free access", freeState, e)
	}
	tx := appleTransaction(f, i, time.Now())
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal("sync replay", e)
	}
	state, e := r.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || state.Status != "ACTIVE" || state.Capacity != 2 {
		t.Fatalf("state %+v %v", state, e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.CreateBranch(context.Background(), f.owner, f.brand, model.CreateBranchRequest{Name: "Additional"})
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	ok, denied := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, repository.ErrAppleCapacity) {
			denied++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || denied != 1 {
		t.Fatalf("concurrent cap successes=%d denied=%d", ok, denied)
	}
	other := accountDeletionFixture(t, p, "")
	lateReferral := req
	lateReferral.ReferralCode = fmt.Sprintf("CLOSE-%d", other.brand)
	if _, e = r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, uuid.NewString(), lateReferral, "Xcode"); !errors.Is(e, repository.ErrReferralCodeInvalid) {
		t.Fatal("late referral restarted introductory discounts", e)
	}
	_, e = r.AppleClaim(t.Context(), other.owner, other.brand, tx, "")
	if e == nil {
		t.Fatal("other brand restored chain")
	}
	// The MP checkout path cannot sell a second provider to this brand.
	if _, _, e = r.ReserveSubscriptionCheckout(t.Context(), f.owner, f.brand, uuid.NewString(), 15000, 20000); !errors.Is(e, repository.ErrBillingProviderConflict) {
		t.Fatal("MP coexistence", e)
	}
}
func TestPostgresAppleExpiryRevocationAndExternalChanges(t *testing.T) {
	p, r, f := appleFixture(t)
	i := appleIntent(t, r, f, 1)
	now := time.Now().UTC()
	tx := appleTransaction(f, i, now)
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	r.Now = func() time.Time { return time.UnixMilli(tx.ExpiresDate).AddDate(0, 0, 8) }
	var hash []byte
	if e := p.QueryRow(t.Context(), `SELECT qr_hash FROM usuarios WHERE id=$1`, f.customer).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	if _, e := r.CreatePreview(t.Context(), f.owner, model.MovementPreviewRequest{BranchID: f.branch, Operation: "ACUMULACION"}, hash, []byte("fingerprint")); !errors.Is(e, repository.ErrAppleReadOnly) {
		t.Fatal("expired movement accepted", e)
	}
	r.Now = func() time.Time { return now }
	revoked := tx
	revoked.RevocationDate = now.UnixMilli()
	revoked.SignedDate = now.Add(time.Second).UnixMilli()
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, revoked, nil); e != nil {
		t.Fatal(e)
	}
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	state, e := r.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || state.Status != "REVOKED" {
		t.Fatalf("old transaction revived refund %+v %v", state, e)
	}
	external := tx
	external.TransactionID = uuid.NewString()
	external.PurchaseDate = now.Add(time.Minute).UnixMilli()
	external.SignedDate = now.Add(time.Minute).UnixMilli()
	external.ProductID = "com.puntazo.app.merchant.puntos.1.monthly"
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, external, nil); e != nil {
		t.Fatal(e)
	}
	state, e = r.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || state.Restriction != "PROGRAM_CONFLICT" {
		t.Fatalf("external program changed ledger %+v %v", state, e)
	}
	var program string
	if e = p.QueryRow(t.Context(), `SELECT tipo FROM programas_fidelidad WHERE marca_id=$1 AND activo`, f.brand).Scan(&program); e != nil || program != "SELLOS" {
		t.Fatal("program mutated")
	}
}
func TestPostgresAppleTrialAndReferralOfferMapping(t *testing.T) {
	p, r, f := appleFixture(t)
	req := model.ApplePurchaseIntentRequest{ProductID: "com.puntazo.app.merchant.sellos.1.monthly"}
	if _, e := p.Exec(t.Context(), `UPDATE marcas SET created_at=now() WHERE id=$1`, f.brand); e != nil {
		t.Fatal(e)
	}
	if _, e := r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, uuid.NewString(), req, "Xcode"); !errors.Is(e, repository.ErrAppleTrialActive) {
		t.Fatal("trial ignored", e)
	}
	_, e := p.Exec(t.Context(), `UPDATE marcas SET created_at=now()-interval '40 days' WHERE id=$1`, f.brand)
	if e != nil {
		t.Fatal(e)
	}
	source := accountDeletionFixture(t, p, "")
	var campaign int64
	if e = p.QueryRow(t.Context(), `SELECT id FROM referral_campaigns WHERE name=$1`, fmt.Sprintf("Closing campaign %d", source.brand)).Scan(&campaign); e != nil {
		t.Fatal(e)
	}
	req.ReferralCode = fmt.Sprintf("CLOSE-%d", source.brand)
	if _, e = r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, uuid.NewString(), req, "Xcode"); !errors.Is(e, repository.ErrAppleOfferUnavailable) {
		t.Fatal("full-price fallback allowed", e)
	}
	if _, e = p.Exec(t.Context(), `INSERT INTO apple_referral_offers(campaign_id,product_id,environment,offer_id,code,discount_bps,discount_charges,enabled) VALUES($1,$2,'Xcode','refer50','LOCAL-CODE',5000,3,true)`, campaign, req.ProductID); e != nil {
		t.Fatal(e)
	}
	intent, e := r.CreateApplePurchaseIntent(t.Context(), f.owner, f.brand, uuid.NewString(), req, "Xcode")
	if e != nil || intent.Offer == nil || intent.Offer.OfferID != "refer50" {
		t.Fatalf("mapped offer %+v %v", intent, e)
	}
	tx := appleTransaction(f, intent, time.Now())
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); !errors.Is(e, repository.ErrAppleOfferUnavailable) {
		t.Fatal("promo bypass by full-price receipt", e)
	}
	tx.OfferIdentifier = "refer50"
	tx.OfferType = 3
	tx.Price = 2500
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	var currency string
	var reward int64
	if e = p.QueryRow(t.Context(), `SELECT currency,reward_milliunits FROM apple_referral_charges WHERE transaction_id=$1`, tx.TransactionID).Scan(&currency, &reward); e != nil || currency != "USD" || reward != 500 {
		t.Fatalf("provider currency ledger %s %d %v", currency, reward, e)
	}
	tx.RevocationDate = time.Now().UnixMilli()
	tx.SignedDate = time.Now().Add(time.Second).UnixMilli()
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = p.QueryRow(t.Context(), `SELECT status FROM apple_referral_charges WHERE transaction_id=$1`, tx.TransactionID).Scan(&status); e != nil || status != "VOID" {
		t.Fatal("refund reward not reversed", status, e)
	}
	staleRefund := tx
	restored := tx
	restored.RevocationDate = 0
	restored.RevocationReversed = true
	restored.SignedDate = tx.SignedDate + 1000
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, restored, nil); e != nil {
		t.Fatal(e)
	}
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, staleRefund, nil); e != nil {
		t.Fatal(e)
	}
	if e = p.QueryRow(t.Context(), `SELECT status FROM apple_referral_charges WHERE transaction_id=$1`, tx.TransactionID).Scan(&status); e != nil || status != "PENDING" {
		t.Fatal("late refund undid reversal", status, e)
	}
	newerRefund := restored
	newerRefund.RevocationReversed = false
	newerRefund.RevocationDate = restored.SignedDate + 1000
	newerRefund.SignedDate += 2000
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, newerRefund, nil); e != nil {
		t.Fatal(e)
	}
	if e = r.SaveAppleTransaction(t.Context(), f.owner, f.brand, restored, nil); e != nil {
		t.Fatal(e)
	}
	if e = p.QueryRow(t.Context(), `SELECT status FROM apple_referral_charges WHERE transaction_id=$1`, tx.TransactionID).Scan(&status); e != nil || status != "VOID" {
		t.Fatal("late reversal revived refunded reward", status, e)
	}

}
func TestPostgresAppleInboxDurableDeduplication(t *testing.T) {
	p, r, _ := appleFixture(t)
	n := model.AppleNotification{NotificationUUID: uuid.NewString()}
	n.Data.Environment = "Xcode"
	for k := 0; k < 2; k++ {
		if e := r.StoreAppleNotification(t.Context(), n, "signed-payload"); e != nil {
			t.Fatal(e)
		}
	}
	items, e := r.LeaseAppleNotifications(t.Context(), "Xcode")
	if e != nil || len(items) != 1 {
		t.Fatal("inbox duplicate", len(items), e)
	}
	if _, e = r.LeaseAppleNotifications(t.Context(), "Xcode"); e != nil {
		t.Fatal(e)
	}
	if e = r.CompleteAppleNotification(t.Context(), items[0], true); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = p.QueryRow(t.Context(), `SELECT count(*) FROM apple_notification_inbox WHERE completed_at IS NOT NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatal("completion not durable", count, e)
	}
}

type localAppleProvider struct {
	t       model.AppleTransaction
	enabled bool
}

func (p *localAppleProvider) PurchasesEnabled() bool { return p.enabled }
func (p *localAppleProvider) Enabled() bool          { return p.enabled }
func (*localAppleProvider) Environment() string      { return "Xcode" }
func (p *localAppleProvider) VerifyTransaction(context.Context, string) (model.AppleTransaction, error) {
	return p.t, nil
}
func (p *localAppleProvider) VerifyNotification(context.Context, string) (model.AppleNotification, error) {
	return model.AppleNotification{}, errors.New("not expected")
}
func (p *localAppleProvider) SubscriptionStatuses(context.Context, string) ([]model.AppleSubscriptionStatus, error) {
	return nil, errors.New("offline")
}
func (p *localAppleProvider) SetAppAccountToken(context.Context, string, string) error {
	return errors.New("offline")
}
func TestPostgresAppleSyncServiceFreeDefault(t *testing.T) {
	_, r, f := appleFixture(t)
	s := service.New(r, nil, config.Config{})
	state, e := s.BrandBilling(t.Context(), f.owner, f.brand)
	if e != nil || state.Mode != "FREE" || state.Restriction != "" {
		t.Fatal("free edition blocked", state, e)
	}
	catalog, e := s.AppleBillingCatalog(t.Context(), f.owner, f.brand)
	if e != nil || catalog.Enabled || catalog.EligibleToPurchase || len(catalog.Products) != 20 {
		t.Fatal("disabled provider catalog", catalog, e)
	}
	i := appleIntent(t, r, f, 1)
	p := &localAppleProvider{t: appleTransaction(f, i, time.Now()), enabled: true}
	s.AppleBilling = p
	state, e = s.SyncApplePurchase(t.Context(), f.owner, f.brand, model.AppleSyncRequest{SignedTransaction: "verified-by-injected-provider"})
	if e != nil || state.Mode != "APPLE" {
		t.Fatal("service sync", state, e)
	}
	p.enabled = false
	if _, e = s.CreateAppleBillingIntent(t.Context(), f.owner, f.brand, uuid.NewString(), model.ApplePurchaseIntentRequest{ProductID: applebilling.Catalog()[0].ProductID}); !errors.Is(e, service.ErrBillingUnavailable) {
		t.Fatal("disabled provider sold", e)
	}
}

func TestPostgresAppleCodeClaimCannotBeReassigned(t *testing.T) {
	p, r, f := appleFixture(t)
	other := accountDeletionFixture(t, p, "")
	_, e := p.Exec(t.Context(), `UPDATE marcas SET created_at=now()-interval '40 days' WHERE id=$1`, other.brand)
	if e != nil {
		t.Fatal(e)
	}
	first := appleIntent(t, r, f, 1)
	second := appleIntent(t, r, other, 1)
	for _, i := range []model.ApplePurchaseIntent{first, second} {
		if _, e = p.Exec(t.Context(), `UPDATE apple_purchase_intents SET offer_id='referral-code' WHERE id=$1`, i.IntentID); e != nil {
			t.Fatal(e)
		}
	}
	tx := appleTransaction(f, first, time.Now())
	tx.AppAccountToken = ""
	tx.OfferIdentifier = "referral-code"
	tx.OfferType = 3
	token, e := r.AppleClaim(t.Context(), f.owner, f.brand, tx, first.IntentID)
	if e != nil || token != first.AppAccountToken {
		t.Fatal("claim", e)
	}
	if _, e = r.AppleClaim(t.Context(), other.owner, other.brand, tx, second.IntentID); !errors.Is(e, repository.ErrAppleChainConflict) {
		t.Fatal("reassigned reserved code chain", e)
	}
	tx.AppAccountToken = second.AppAccountToken
	if e = r.SaveAppleTransaction(t.Context(), other.owner, other.brand, tx, nil); !errors.Is(e, repository.ErrAppleChainConflict) {
		t.Fatal("reservation bypass", e)
	}
}
func TestPostgresAppleDeletionAndNotificationsNeverResurrect(t *testing.T) {
	p, r, f := appleFixture(t)
	i := appleIntent(t, r, f, 1)
	tx := appleTransaction(f, i, time.Now())
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := r.AnonymizeAccount(t.Context(), f.owner, 1); e != nil {
		t.Fatal(e)
	}
	renewal := tx
	renewal.TransactionID = uuid.NewString()
	renewal.PurchaseDate = time.Now().Add(time.Hour).UnixMilli()
	renewal.ExpiresDate = time.Now().AddDate(0, 2, 0).UnixMilli()
	renewal.SignedDate = time.Now().Add(time.Hour).UnixMilli()
	if e := r.SaveAppleTransaction(t.Context(), 0, f.brand, renewal, nil); e != nil {
		t.Fatal("renewal provenance", e)
	}
	var active bool
	if e := p.QueryRow(t.Context(), `SELECT activo FROM marcas WHERE id=$1`, f.brand).Scan(&active); e != nil || active {
		t.Fatal("brand resurrected", e)
	}
	if _, e := r.AppleClaim(t.Context(), f.owner, f.brand, renewal, ""); !errors.Is(e, repository.ErrForbidden) {
		t.Fatal("deleted owner regained access", e)
	}
}

type appleNotificationProvider struct {
	localAppleProvider
	events map[string]model.AppleNotification
}

func (p *appleNotificationProvider) VerifyNotification(_ context.Context, signed string) (model.AppleNotification, error) {
	n, ok := p.events[signed]
	if !ok {
		return n, errors.New("invalid signed fixture")
	}
	return n, nil
}
func TestPostgresAppleNotificationWorkerReplaysAndRefund(t *testing.T) {
	p, r, f := appleFixture(t)
	i := appleIntent(t, r, f, 1)
	now := time.Now()
	tx := appleTransaction(f, i, now)
	provider := &appleNotificationProvider{localAppleProvider: localAppleProvider{enabled: true}, events: map[string]model.AppleNotification{}}
	svc := service.New(r, nil, config.Config{})
	svc.AppleBilling = provider
	enqueue := func(name string, t model.AppleTransaction) {
		n := model.AppleNotification{NotificationUUID: uuid.NewString(), Transaction: &t}
		n.Data.Environment = "Xcode"
		provider.events[name] = n
		if e := svc.ReceiveAppleNotification(context.Background(), name); e != nil {
			panic(e)
		}
	}
	enqueue("purchased", tx)
	if e := svc.ProcessAppleNotifications(t.Context()); e != nil {
		t.Fatal(e)
	}
	latest := tx
	latest.TransactionID = uuid.NewString()
	latest.PurchaseDate = now.Add(time.Hour).UnixMilli()
	latest.ExpiresDate = now.AddDate(0, 2, 0).UnixMilli()
	latest.SignedDate = now.Add(time.Hour).UnixMilli()
	enqueue("renewed", latest)
	enqueue("late-old", tx)
	if e := svc.ProcessAppleNotifications(t.Context()); e != nil {
		t.Fatal(e)
	}
	state, e := r.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || state.CoveredUntil == nil || state.CoveredUntil.UnixMilli() != latest.ExpiresDate {
		t.Fatal("old event replaced renewal", state, e)
	}
	refunded := latest
	refunded.RevocationDate = now.Add(2 * time.Hour).UnixMilli()
	refunded.SignedDate = now.Add(2 * time.Hour).UnixMilli()
	enqueue("refund", refunded)
	enqueue("late-unrevoked", latest)
	if e := svc.ProcessAppleNotifications(t.Context()); e != nil {
		t.Fatal(e)
	}
	state, e = r.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || state.Status != "REVOKED" {
		t.Fatal("refund worker", state, e)
	}
	unknown := latest
	unknown.OriginalTransactionID = uuid.NewString()
	unknown.TransactionID = uuid.NewString()
	unknown.AppAccountToken = uuid.NewString()
	enqueue("unbound", unknown)
	if e := svc.ProcessAppleNotifications(t.Context()); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = p.QueryRow(t.Context(), `SELECT count(*) FROM apple_notification_inbox WHERE completed_at IS NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatal("unknown chain granted or dropped", count, e)
	}
}

func TestPostgresAppleReferralSequenceUsesPurchaseOrder(t *testing.T) {
	p, r, f := appleFixture(t)
	source := accountDeletionFixture(t, p, "")
	var campaign, code int64
	if e := p.QueryRow(t.Context(), `SELECT campaign_id,id FROM referral_codes WHERE source_brand_id=$1`, source.brand).Scan(&campaign, &code); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(t.Context(), `INSERT INTO referral_attributions(brand_id,code_id,campaign_id,source_kind,source_brand_id,discount_bps,discount_charges,reward_bps,reward_charges) VALUES($1,$2,$3,'MERCHANT',$4,0,0,2000,1)`, f.brand, code, campaign, source.brand); e != nil {
		t.Fatal(e)
	}
	i := appleIntent(t, r, f, 1)
	later := appleTransaction(f, i, time.Now())
	earlier := later
	earlier.TransactionID = uuid.NewString()
	earlier.PurchaseDate = time.UnixMilli(later.PurchaseDate).AddDate(0, -1, 0).UnixMilli()
	earlier.ExpiresDate = later.PurchaseDate
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, later, nil); e != nil {
		t.Fatal(e)
	}
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, earlier, nil); e != nil {
		t.Fatal(e)
	}
	var firstIndex, secondIndex int
	var firstReward, secondReward int64
	if e := p.QueryRow(t.Context(), `SELECT paid_index,reward_milliunits FROM apple_referral_charges WHERE transaction_id=$1`, earlier.TransactionID).Scan(&firstIndex, &firstReward); e != nil {
		t.Fatal(e)
	}
	if e := p.QueryRow(t.Context(), `SELECT paid_index,reward_milliunits FROM apple_referral_charges WHERE transaction_id=$1`, later.TransactionID).Scan(&secondIndex, &secondReward); e != nil {
		t.Fatal(e)
	}
	if firstIndex != 1 || secondIndex != 2 || firstReward != 998 || secondReward != 0 {
		t.Fatalf("payment order idx=%d/%d reward=%d/%d", firstIndex, secondIndex, firstReward, secondReward)
	}
}

type stoppedApplePurchases struct{ localAppleProvider }

func (*stoppedApplePurchases) PurchasesEnabled() bool { return false }
func TestPostgresAppleStopNewPurchasesKeepsSync(t *testing.T) {
	_, r, f := appleFixture(t)
	intent := appleIntent(t, r, f, 1)
	provider := &stoppedApplePurchases{localAppleProvider{t: appleTransaction(f, intent, time.Now()), enabled: true}}
	svc := service.New(r, nil, config.Config{})
	svc.AppleBilling = provider
	catalog, e := svc.AppleBillingCatalog(t.Context(), f.owner, f.brand)
	if e != nil || catalog.Enabled || catalog.EligibleToPurchase {
		t.Fatal("new purchases not stopped", catalog, e)
	}
	if _, e = svc.CreateAppleBillingIntent(t.Context(), f.owner, f.brand, uuid.NewString(), model.ApplePurchaseIntentRequest{ProductID: intent.ProductID}); !errors.Is(e, service.ErrBillingUnavailable) {
		t.Fatal("new purchase allowed", e)
	}
	state, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, model.AppleSyncRequest{SignedTransaction: "verified"})
	if e != nil || state.Status != "ACTIVE" {
		t.Fatal("stop purchases disabled existing receipt", state, e)
	}
}

func TestPostgresAppleSandboxCannotGrantProductionRights(t *testing.T) {
	p, r, f := appleFixture(t)
	intent := appleIntent(t, r, f, 1)
	tx := appleTransaction(f, intent, time.Now())
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	production := repository.New(p) // Empty field intentionally defaults to Production.
	state, e := production.BrandBillingState(t.Context(), f.owner, f.brand)
	if e != nil || state.Restriction != "ENVIRONMENT_CONFLICT" || state.Mode != "APPLE" || state.Status != "READ_ONLY" {
		t.Fatal("local receipt granted production or fell back free", state, e)
	}
	if e = production.CheckAppleBillingWrite(t.Context(), f.owner, f.brand); !errors.Is(e, repository.ErrAppleEnvironmentConflict) {
		t.Fatal("cross-channel foreign environment writes", e)
	}
	if e = production.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); !errors.Is(e, repository.ErrAppleEnvironmentConflict) {
		t.Fatal("production persisted local receipt", e)
	}
}

func TestPostgresApplePeriodicReconciliationLease(t *testing.T) {
	p, r, f := appleFixture(t)
	intent := appleIntent(t, r, f, 1)
	tx := appleTransaction(f, intent, time.Now())
	if e := r.SaveAppleTransaction(t.Context(), f.owner, f.brand, tx, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(t.Context(), `UPDATE apple_subscription_chains SET next_reconcile_at=now()-interval '1 minute'`); e != nil {
		t.Fatal(e)
	}
	chains, e := r.LeaseAppleReconciliation(t.Context(), "Xcode")
	if e != nil || len(chains) != 1 {
		t.Fatal("reconciliation lease", chains, e)
	}
	again, e := r.LeaseAppleReconciliation(t.Context(), "Xcode")
	if e != nil || len(again) != 0 {
		t.Fatal("duplicate reconciliation lease", again, e)
	}
	if e = r.FinishAppleReconciliation(t.Context(), chains[0], true); e != nil {
		t.Fatal("reconciliation reschedule", e)
	}
	var future bool
	if e = p.QueryRow(t.Context(), `SELECT next_reconcile_at>now()+interval '4 minutes' AND reconcile_lease_until IS NULL FROM apple_subscription_chains`).Scan(&future); e != nil || !future {
		t.Fatal("reconciliation did not reschedule", future, e)
	}
}

func TestPostgresAppleBillingRollbackProtectsDurableData(t *testing.T) {
	down, e := os.ReadFile(filepath.Join("..", "..", "migrations", "0045_apple_billing.down.sql"))
	if e != nil {
		t.Fatal(e)
	}
	t.Run("populated requires forward fix", func(t *testing.T) {
		pool, repo, f := appleFixture(t)
		intent := appleIntent(t, repo, f, 1)
		// A pending intent already reserves a provider and can await Ask-to-Buy:
		// no completed transaction is needed for this history to require preservation.
		if _, e := pool.Exec(t.Context(), string(down)); e == nil {
			t.Fatal("rollback discarded pending purchase")
		}
		var saved string
		if e := pool.QueryRow(t.Context(), `SELECT id::text FROM apple_purchase_intents WHERE id=$1`, intent.IntentID).Scan(&saved); e != nil || saved != intent.IntentID {
			t.Fatal("rollback changed durable intent", saved, e)
		}
	})
	t.Run("empty permits historical rollback", func(t *testing.T) {
		pool := referralBillingPool(t)
		if _, e := pool.Exec(t.Context(), string(down)); e != nil {
			t.Fatal("empty rollback", e)
		}
		var removed bool
		// CI also migrates public. Unqualified names would fall through search_path
		// to those untouched tables after this fixture's own tables are dropped.
		if e := pool.QueryRow(t.Context(), `SELECT to_regclass(format('%I.apple_billing_accounts', current_schema())) IS NULL AND to_regclass(format('%I.apple_claim_reservations', current_schema())) IS NULL AND to_regclass(format('%I.apple_transactions', current_schema())) IS NULL`).Scan(&removed); e != nil || !removed {
			t.Fatal("empty tables not removed", removed, e)
		}
	})
}

type restoreAppleProvider struct {
	t         model.AppleTransaction
	submitted *model.AppleTransaction
	sets      int
	invalid   bool
}

func (*restoreAppleProvider) Enabled() bool          { return true }
func (*restoreAppleProvider) PurchasesEnabled() bool { return false }
func (*restoreAppleProvider) Environment() string    { return "Xcode" }
func (p *restoreAppleProvider) VerifyTransaction(_ context.Context, signed string) (model.AppleTransaction, error) {
	if p.invalid || signed != "verified" {
		return model.AppleTransaction{}, errors.New("bad signature")
	}
	if p.submitted != nil {
		return *p.submitted, nil
	}
	return p.t, nil
}
func (*restoreAppleProvider) VerifyNotification(context.Context, string) (model.AppleNotification, error) {
	return model.AppleNotification{}, errors.New("not expected")
}
func (p *restoreAppleProvider) SubscriptionStatuses(_ context.Context, original string) ([]model.AppleSubscriptionStatus, error) {
	if original != p.t.OriginalTransactionID {
		return nil, errors.New("wrong original")
	}
	return []model.AppleSubscriptionStatus{{Transaction: p.t}}, nil
}
func (p *restoreAppleProvider) SetAppAccountToken(_ context.Context, _ string, token string) error {
	p.sets++
	p.t.AppAccountToken = token
	return nil
}

func TestPostgresAppleExplicitRestore(t *testing.T) {
	p, r, f := appleFixture(t)
	expired := appleIntent(t, r, f, 1)
	if _, e := p.Exec(t.Context(), `UPDATE apple_purchase_intents SET expires_at=now()-interval '1 minute' WHERE id=$1`, expired.IntentID); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(t.Context(), `UPDATE marcas SET created_at=now() WHERE id=$1`, f.brand); e != nil {
		t.Fatal(e)
	}
	purchase := appleTransaction(f, model.ApplePurchaseIntent{ProductID: "com.puntazo.app.merchant.sellos.1.monthly"}, time.Now())
	provider := &restoreAppleProvider{t: purchase}
	svc := service.New(r, nil, config.Config{})
	svc.AppleBilling = provider
	req := model.AppleSyncRequest{SignedTransaction: "verified"}
	if _, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, req); !errors.Is(e, repository.ErrAppleIntentRequired) {
		t.Fatal("automatic claim accepted", e)
	}
	req.IntentID = expired.IntentID
	if _, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, req); !errors.Is(e, repository.ErrAppleIntentRequired) {
		t.Fatal("expired intent accepted", e)
	}
	req.Restore = true
	if _, e := svc.SyncApplePurchase(t.Context(), f.customer, f.brand, req); !errors.Is(e, repository.ErrForbidden) {
		t.Fatal("nonowner restore", e)
	}
	provider.invalid = true
	if _, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, req); !errors.Is(e, service.ErrAppleTransactionInvalid) {
		t.Fatal("invalid signature", e)
	}
	provider.invalid = false
	state, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, req)
	if e != nil || state.Mode != "APPLE" || provider.sets != 1 {
		t.Fatal("restore with trial active and purchases stopped", state, provider.sets, e)
	}
	if _, e = svc.SyncApplePurchase(t.Context(), f.owner, f.brand, req); e != nil || provider.sets != 1 {
		t.Fatal("restore replay", e)
	}
	other := accountDeletionFixture(t, p, "")
	if _, e = svc.SyncApplePurchase(t.Context(), other.owner, other.brand, req); !errors.Is(e, repository.ErrAppleChainConflict) {
		t.Fatal("chain stolen", e)
	}
}

func TestPostgresAppleRestoreRejectsBeforeAssignment(t *testing.T) {
	for _, which := range []string{"program", "capacity", "referral", "mp", "foreign-token", "reserved"} {
		t.Run(which, func(t *testing.T) {
			p, r, f := appleFixture(t)
			purchase := appleTransaction(f, model.ApplePurchaseIntent{ProductID: "com.puntazo.app.merchant.sellos.1.monthly"}, time.Now())
			var expected error
			switch which {
			case "program":
				purchase.ProductID = "com.puntazo.app.merchant.puntos.1.monthly"
				expected = repository.ErrAppleProductConflict
			case "capacity":
				if _, e := r.CreateBranch(t.Context(), f.owner, f.brand, model.CreateBranchRequest{Name: "Extra"}); e != nil {
					t.Fatal(e)
				}
				expected = repository.ErrAppleCapacity
			case "referral":
				source := accountDeletionFixture(t, p, "")
				_, e := p.Exec(t.Context(), `INSERT INTO referral_attributions(brand_id,code_id,campaign_id,source_kind,source_brand_id,discount_bps,discount_charges,reward_bps,reward_charges) SELECT $1,id,campaign_id,'MERCHANT',$2,5000,3,0,0 FROM referral_codes WHERE source_brand_id=$2`, f.brand, source.brand)
				if e != nil {
					t.Fatal(e)
				}
				expected = repository.ErrAppleOfferUnavailable
			case "mp":
				if _, _, e := r.ReserveSubscriptionCheckout(t.Context(), f.owner, f.brand, uuid.NewString(), 15000, 20000); e != nil {
					t.Fatal(e)
				}
				expected = repository.ErrBillingProviderConflict
			case "foreign-token":
				purchase.AppAccountToken = uuid.NewString()
				expected = repository.ErrAppleChainConflict
			case "reserved":
				other := accountDeletionFixture(t, p, "")
				if _, e := r.AppleRestoreClaim(t.Context(), other.owner, other.brand, purchase); e != nil {
					t.Fatal(e)
				}
				expected = repository.ErrAppleChainConflict
			}
			provider := &restoreAppleProvider{t: purchase}
			svc := service.New(r, nil, config.Config{})
			svc.AppleBilling = provider
			_, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, model.AppleSyncRequest{SignedTransaction: "verified", Restore: true})
			if !errors.Is(e, expected) || provider.sets != 0 {
				t.Fatal("assigned despite conflict", e, provider.sets)
			}
		})
	}
}

func TestPostgresAppleRestoreCanonicalAndConcurrentClaims(t *testing.T) {
	p, r, f := appleFixture(t)
	other := accountDeletionFixture(t, p, "")
	purchase := appleTransaction(f, model.ApplePurchaseIntent{ProductID: "com.puntazo.app.merchant.sellos.1.monthly"}, time.Now())
	canonical := purchase
	canonical.AppAccountToken = uuid.NewString()
	provider := &restoreAppleProvider{t: canonical, submitted: &purchase}
	svc := service.New(r, nil, config.Config{})
	svc.AppleBilling = provider
	_, e := svc.SyncApplePurchase(t.Context(), f.owner, f.brand, model.AppleSyncRequest{SignedTransaction: "verified", Restore: true})
	if !errors.Is(e, repository.ErrAppleChainConflict) || provider.sets != 0 {
		t.Fatal("canonical foreign token", e, provider.sets)
	}
	canonical = purchase
	canonical.ProductID = "com.puntazo.app.merchant.puntos.1.monthly"
	provider.t = canonical
	_, e = svc.SyncApplePurchase(t.Context(), f.owner, f.brand, model.AppleSyncRequest{SignedTransaction: "verified", Restore: true})
	if !errors.Is(e, repository.ErrAppleProductConflict) || provider.sets != 0 {
		t.Fatal("canonical incompatible product", e, provider.sets)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, v := range []deletionFixture{f, other} {
		go func(v deletionFixture) {
			<-start
			_, err := r.AppleRestoreClaim(context.Background(), v.owner, v.brand, purchase)
			results <- err
		}(v)
	}
	close(start)
	ok, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			ok++
		} else if errors.Is(err, repository.ErrAppleChainConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("claim concurrency", ok, conflict)
	}
}

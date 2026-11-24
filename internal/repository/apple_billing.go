package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"clientesFrecuentes/internal/applebilling"
	"clientesFrecuentes/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrAppleEnvironmentConflict = errors.New("Apple subscription belongs to another server environment")
	ErrAppleChainConflict       = errors.New("Apple subscription belongs to another brand")
	ErrAppleIntentRequired      = errors.New("authorized Apple purchase intent required")
	ErrAppleOfferUnavailable    = errors.New("configured Apple referral offer unavailable")
	ErrAppleTrialActive         = errors.New("Puntazo free trial has not ended")
	ErrAppleCapacity            = errors.New("Apple subscription capacity exceeded")
	ErrAppleProductConflict     = errors.New("Apple subscription program conflict")
	ErrAppleReadOnly            = errors.New("Apple subscription is read only")
	ErrBillingProviderConflict  = errors.New("another billing provider owns this brand")
)

type AppleSnapshot struct {
	State        model.BrandBillingState
	Token        string
	BrandProgram string
	Revoked      bool
	SignedAt     time.Time
}

// BillingState permits all active brand members to inspect their shared rights.
func (r *Repository) BrandBillingState(ctx context.Context, actor, brand int64) (model.BrandBillingState, error) {
	var allowed bool
	e := r.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM membresias_marca mm JOIN usuarios u ON u.id=mm.usuario_id WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo AND u.activo AND u.deleted_at IS NULL)`, actor, brand).Scan(&allowed)
	if e != nil {
		return model.BrandBillingState{}, e
	}
	if !allowed {
		return model.BrandBillingState{}, ErrForbidden
	}
	snapshot, e := appleSnapshot(ctx, r.Pool, brand, r.Now(), r.appleEnvironment())
	return snapshot.State, e
}
func appleSnapshot(ctx context.Context, q billingQuerier, brand int64, now time.Time, environments ...string) (AppleSnapshot, error) {
	var out AppleSnapshot
	var registration time.Time
	activeEnvironment := ""
	environment := "Production"
	if len(environments) > 0 && environments[0] != "" {
		environment = environments[0]
	}
	var mp bool
	e := q.QueryRow(ctx, `SELECT COALESCE(a.mode,'FREE'),COALESCE(a.app_account_token::text,''),COALESCE(a.active_environment,''),p.tipo,m.created_at,(SELECT count(*) FROM sucursales s WHERE s.marca_id=m.id AND s.activo AND s.deleted_at IS NULL),EXISTS(SELECT 1 FROM suscripciones_marca s WHERE s.marca_id=m.id AND s.estado<>'CANCELLED') FROM marcas m JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo LEFT JOIN apple_billing_accounts a ON a.marca_id=m.id WHERE m.id=$1 AND m.activo AND m.deleted_at IS NULL`, brand).Scan(&out.State.Mode, &out.Token, &activeEnvironment, &out.BrandProgram, &registration, &out.State.ActiveBranches, &mp)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if e != nil {
		return out, e
	}
	end := TrialEnd(registration)
	out.State.TrialEndsAt = &end
	if out.State.Mode != "APPLE" {
		out.State.Status = "FREE"
		if mp {
			out.State.Mode = "MERCADO_PAGO"
			out.State.Status = "ACTIVE"
		}
		return out, nil
	}
	if activeEnvironment != environment {
		out.State.Status = "READ_ONLY"
		out.State.Restriction = "ENVIRONMENT_CONFLICT"
		return out, nil
	}
	var product, program string
	var capacity int
	var expiry time.Time
	var revoked *time.Time
	var upgraded bool
	var auto *bool
	var pending *string
	var appleGrace *time.Time
	e = q.QueryRow(ctx, `SELECT t.product_id,t.program_type,t.capacity,t.expires_at,t.revoked_at,t.upgraded,t.signed_at,r.auto_renew,r.pending_product_id,r.apple_grace_ends_at FROM apple_billing_accounts a JOIN apple_subscription_chains c ON c.marca_id=a.marca_id AND c.environment=a.active_environment JOIN apple_transactions t ON t.environment=c.environment AND t.original_transaction_id=c.original_transaction_id LEFT JOIN apple_renewal_state r ON r.environment=c.environment AND r.original_transaction_id=c.original_transaction_id WHERE a.marca_id=$1 ORDER BY t.purchase_at DESC,t.expires_at DESC,t.signed_at DESC LIMIT 1`, brand).Scan(&product, &program, &capacity, &expiry, &revoked, &upgraded, &out.SignedAt, &auto, &pending, &appleGrace)
	if errors.Is(e, pgx.ErrNoRows) {
		out.State.Status = "NOT_SUBSCRIBED"
		out.State.Restriction = "EXPIRED"
		return out, nil
	}
	if e != nil {
		return out, e
	}
	// An upgraded latest transaction means its replacement has not yet been
	// recorded. Never fall back to older, potentially stale entitlements.
	// Revocation must take precedence over this temporary missing state.
	if upgraded && revoked == nil {
		out.State.Status = "NOT_SUBSCRIBED"
		out.State.Restriction = "EXPIRED"
		return out, nil
	}
	out.State = DeriveAppleState(out.State, product, program, out.BrandProgram, capacity, expiry, appleGrace, revoked, auto, pending, now)
	out.Revoked = revoked != nil
	return out, nil
}

// DeriveAppleState is shared by HTTP state and transactional write guards.
func DeriveAppleState(s model.BrandBillingState, product, program, currentProgram string, capacity int, expiry time.Time, appleGrace, revoked *time.Time, auto *bool, pending *string, now time.Time) model.BrandBillingState {
	s.ProductID = product
	s.ProgramType = program
	s.Capacity = capacity
	s.CoveredUntil = &expiry
	s.AutoRenew = auto
	if pending != nil {
		s.PendingProductID = *pending
	}
	covered := expiry
	if appleGrace != nil && appleGrace.After(covered) {
		covered = *appleGrace
	}
	grace := covered.AddDate(0, 0, 7)
	s.GraceEndsAt = &grace
	s.Status = "ACTIVE"
	if now.After(expiry) {
		s.Status = "GRACE"
	}
	if !now.Before(grace) {
		s.Status = "READ_ONLY"
		s.Restriction = "EXPIRED"
	}
	if program != currentProgram {
		s.Status = "PROGRAM_CONFLICT"
		s.Restriction = "PROGRAM_CONFLICT"
	}
	if s.ActiveBranches > capacity {
		s.Status = "OVER_CAPACITY"
		s.Restriction = "OVER_CAPACITY"
	}
	if revoked != nil {
		s.Status = "REVOKED"
		s.Restriction = "REVOKED"
	}
	return s
}
func RequireAppleWrite(ctx context.Context, tx pgx.Tx, brand int64, additionalBranches int, now time.Time, environments ...string) error {
	var locked int64
	if e := tx.QueryRow(ctx, `SELECT id FROM marcas WHERE id=$1 AND activo AND deleted_at IS NULL FOR SHARE`, brand).Scan(&locked); e != nil {
		return e
	}
	// In repeatable-read movement transactions a concurrent billing-account
	// update must serialize/retry rather than use rights from an older snapshot.
	var accountID int64
	lockErr := tx.QueryRow(ctx, `SELECT marca_id FROM apple_billing_accounts WHERE marca_id=$1 FOR SHARE`, brand).Scan(&accountID)
	if lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
		return lockErr
	}
	snapshot, e := appleSnapshot(ctx, tx, brand, now, environments...)
	if e != nil {
		return e
	}
	return appleWriteAllowed(snapshot.State, additionalBranches)
}
func appleWriteAllowed(state model.BrandBillingState, additional int) error {
	if state.Mode != "APPLE" {
		return nil
	}
	if state.Restriction == "ENVIRONMENT_CONFLICT" {
		return ErrAppleEnvironmentConflict
	}
	if state.Restriction == "PROGRAM_CONFLICT" {
		return ErrAppleProductConflict
	}
	if state.Restriction == "OVER_CAPACITY" {
		return ErrAppleCapacity
	}
	if state.Restriction != "" {
		return ErrAppleReadOnly
	}
	if state.ActiveBranches+additional > state.Capacity {
		return ErrAppleCapacity
	}
	return nil
}
func (r *Repository) CheckAppleBillingWrite(ctx context.Context, actor, brand int64) error {
	s, e := r.BrandBillingState(ctx, actor, brand)
	if e != nil {
		return e
	}
	return appleWriteAllowed(s, 0)
}
func requireNoAppleSubscription(ctx context.Context, q billingQuerier, brand int64) error {
	var active bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM apple_billing_accounts WHERE marca_id=$1)`, brand).Scan(&active)
	if e != nil {
		return e
	}
	if active {
		return ErrBillingProviderConflict
	}
	return nil
}
func lockAppleOwner(ctx context.Context, tx pgx.Tx, actor, brand int64) error {
	var id int64
	e := tx.QueryRow(ctx, `SELECT m.id FROM marcas m JOIN membresias_marca mm ON mm.marca_id=m.id JOIN usuarios u ON u.id=mm.usuario_id WHERE m.id=$2 AND m.activo AND m.deleted_at IS NULL AND mm.usuario_id=$1 AND mm.activo AND mm.rol='PROPIETARIO' AND u.activo AND u.deleted_at IS NULL FOR UPDATE OF m`, actor, brand).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrForbidden
	}
	return e
}

// Serialize first binding and reservation across different brand locks.
func lockAppleChain(ctx context.Context, tx pgx.Tx, t model.AppleTransaction) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, t.Environment+":"+t.OriginalTransactionID)
	return err
}
func (r *Repository) CreateApplePurchaseIntent(ctx context.Context, actor, brand int64, key string, req model.ApplePurchaseIntentRequest, environment string) (model.ApplePurchaseIntent, error) {
	var out model.ApplePurchaseIntent
	if environment != r.appleEnvironment() {
		return out, ErrAppleEnvironmentConflict
	}
	p, ok := applebilling.Product(req.ProductID)
	if !ok {
		return out, ErrInvalidRequest
	}
	if _, e := uuid.Parse(key); e != nil {
		return out, ErrInvalidRequest
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = lockAppleOwner(ctx, tx, actor, brand); e != nil {
		return out, e
	}
	snapshot, e := appleSnapshot(ctx, tx, brand, r.Now(), r.appleEnvironment())
	if e != nil {
		return out, e
	}
	if snapshot.State.Mode == "MERCADO_PAGO" {
		return out, ErrBillingProviderConflict
	}
	if p.ProgramType != snapshot.BrandProgram {
		return out, ErrAppleProductConflict
	}
	if p.Capacity < snapshot.State.ActiveBranches {
		return out, ErrAppleCapacity
	}
	fingerprint := applebilling.Fingerprint(req.ProductID, req.ReferralCode)
	var saved []byte
	var offerID, code *string
	e = tx.QueryRow(ctx, `SELECT i.id::text,a.app_account_token::text,i.product_id,i.expires_at,i.offer_id,i.offer_code,i.fingerprint FROM apple_purchase_intents i JOIN apple_billing_accounts a ON a.marca_id=i.marca_id WHERE i.marca_id=$1 AND i.idempotency_key=$2`, brand, key).Scan(&out.IntentID, &out.AppAccountToken, &out.ProductID, &out.ExpiresAt, &offerID, &code, &saved)
	if e == nil {
		if !bytes.Equal(saved, fingerprint) {
			return out, ErrIdempotencyConflict
		}
		if !out.ExpiresAt.After(r.Now()) {
			return out, ErrAppleIntentRequired
		}
		if offerID != nil {
			out.Offer = &model.AppleOffer{OfferID: *offerID}
			if code != nil {
				out.Offer.Code = *code
			}
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if snapshot.State.TrialEndsAt == nil || r.Now().Before(*snapshot.State.TrialEndsAt) {
		return out, ErrAppleTrialActive
	}
	if _, e = tx.Exec(ctx, `INSERT INTO apple_billing_accounts(marca_id,app_account_token) VALUES($1,$2) ON CONFLICT(marca_id) DO NOTHING`, brand, uuid.NewString()); e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT app_account_token::text FROM apple_billing_accounts WHERE marca_id=$1`, brand).Scan(&out.AppAccountToken); e != nil {
		return out, e
	}
	if strings.TrimSpace(req.ReferralCode) != "" {
		var existing *string
		e = tx.QueryRow(ctx, `SELECT c.code FROM referral_attributions a JOIN referral_codes c ON c.id=a.code_id WHERE a.brand_id=$1`, brand).Scan(&existing)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
		if existing != nil && *existing != strings.ToUpper(strings.TrimSpace(req.ReferralCode)) {
			return out, ErrReferralCodeInvalid
		}
		if existing == nil {
			var alreadyPaid bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM apple_transactions t JOIN apple_subscription_chains c USING(environment,original_transaction_id) WHERE c.marca_id=$1 AND t.price_milliunits>0) OR EXISTS(SELECT 1 FROM referral_charges WHERE brand_id=$1)`, brand).Scan(&alreadyPaid)
			if e != nil {
				return out, e
			}
			if alreadyPaid {
				return out, ErrReferralCodeInvalid
			}
		}
		if existing == nil {
			if e = AttributeReferral(ctx, tx, brand, p.ProgramType, req.ReferralCode); e != nil {
				return out, e
			}
		}
	}
	var campaign int64
	var bps, charges, paid int
	e = tx.QueryRow(ctx, `SELECT campaign_id,discount_bps,discount_charges,(SELECT count(*) FROM apple_referral_charges c WHERE c.marca_id=$1 AND c.environment=$2)+(SELECT count(*) FROM referral_charges c WHERE c.brand_id=$1) FROM referral_attributions WHERE brand_id=$1`, brand, environment).Scan(&campaign, &bps, &charges, &paid)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if e == nil && bps > 0 && paid < charges {
		var id, c string
		e = tx.QueryRow(ctx, `SELECT offer_id,code FROM apple_referral_offers WHERE campaign_id=$1 AND product_id=$2 AND environment=$3 AND enabled AND discount_bps=$4 AND discount_charges=$5`, campaign, p.ProductID, environment, bps, charges-paid).Scan(&id, &c)
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ErrAppleOfferUnavailable
		}
		if e != nil {
			return out, e
		}
		out.Offer = &model.AppleOffer{OfferID: id, Code: c}
		offerID = &id
		code = &c
	}
	out.IntentID = uuid.NewString()
	out.ProductID = p.ProductID
	out.ExpiresAt = r.Now().Add(30 * time.Minute)
	_, e = tx.Exec(ctx, `INSERT INTO apple_purchase_intents(id,marca_id,actor_id,idempotency_key,fingerprint,product_id,offer_id,offer_code,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, out.IntentID, brand, actor, key, fingerprint, p.ProductID, offerID, code, out.ExpiresAt)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}

// AppleClaim returns an authorized token before any remote assignment. The brand
// lock and permanent binding are checked again when the verified transaction is saved.
func (r *Repository) AppleClaim(ctx context.Context, actor, brand int64, t model.AppleTransaction, intent string) (string, error) {
	if t.Environment != r.appleEnvironment() {
		return "", ErrAppleEnvironmentConflict
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = lockAppleOwner(ctx, tx, actor, brand); e != nil {
		return "", e
	}
	if e = lockAppleChain(ctx, tx, t); e != nil {
		return "", e
	}
	var token string
	e = tx.QueryRow(ctx, `SELECT app_account_token::text FROM apple_billing_accounts WHERE marca_id=$1`, brand).Scan(&token)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", ErrAppleIntentRequired
	}
	if e != nil {
		return "", e
	}
	var bound int64
	e = tx.QueryRow(ctx, `SELECT marca_id FROM apple_subscription_chains WHERE environment=$1 AND original_transaction_id=$2`, t.Environment, t.OriginalTransactionID).Scan(&bound)
	if e == nil {
		if bound != brand {
			return "", ErrAppleChainConflict
		}
		if t.AppAccountToken != "" && t.AppAccountToken != token {
			return "", ErrAppleChainConflict
		}
		return token, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if t.AppAccountToken != "" {
		if t.AppAccountToken != token {
			return "", ErrAppleChainConflict
		}
		return token, tx.Commit(ctx)
	}
	if _, e = uuid.Parse(intent); e != nil {
		return "", ErrAppleIntentRequired
	}
	var product string
	var offer *string
	e = tx.QueryRow(ctx, `SELECT product_id,offer_id FROM apple_purchase_intents WHERE id=$1 AND marca_id=$2 AND actor_id=$3 AND expires_at>$4`, intent, brand, actor, r.Now()).Scan(&product, &offer)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", ErrAppleIntentRequired
	}
	if e != nil {
		return "", e
	}
	if product != t.ProductID || offer == nil || t.OfferIdentifier != *offer {
		return "", ErrAppleIntentRequired
	}
	_, e = tx.Exec(ctx, `INSERT INTO apple_claim_reservations(environment,original_transaction_id,marca_id,app_account_token) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, t.Environment, t.OriginalTransactionID, brand, token)
	if e != nil {
		return "", e
	}
	var reserved int64
	if e = tx.QueryRow(ctx, `SELECT marca_id FROM apple_claim_reservations WHERE environment=$1 AND original_transaction_id=$2 FOR UPDATE`, t.Environment, t.OriginalTransactionID).Scan(&reserved); e != nil {
		return "", e
	}
	if reserved != brand {
		return "", ErrAppleChainConflict
	}
	return token, tx.Commit(ctx)
}

// AppleRestoreClaim reserves a previously unbound tokenless chain for an
// explicit owner-initiated restore. Possession of a signed transaction is not
// proof of the identity of the Apple account holder.
func (r *Repository) AppleRestoreClaim(ctx context.Context, actor, brand int64, t model.AppleTransaction) (string, error) {
	if t.Environment != r.appleEnvironment() {
		return "", ErrAppleEnvironmentConflict
	}
	p, ok := applebilling.Product(t.ProductID)
	if !ok {
		return "", ErrInvalidRequest
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = lockAppleOwner(ctx, tx, actor, brand); e != nil {
		return "", e
	}
	if e = lockAppleChain(ctx, tx, t); e != nil {
		return "", e
	}
	var bound int64
	e = tx.QueryRow(ctx, `SELECT marca_id FROM apple_subscription_chains WHERE environment=$1 AND original_transaction_id=$2`, t.Environment, t.OriginalTransactionID).Scan(&bound)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if e == nil && bound != brand {
		return "", ErrAppleChainConflict
	}
	alreadyBound := e == nil
	var token string
	e = tx.QueryRow(ctx, `SELECT app_account_token::text FROM apple_billing_accounts WHERE marca_id=$1`, brand).Scan(&token)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if t.AppAccountToken != "" && (token == "" || t.AppAccountToken != token) {
		return "", ErrAppleChainConflict
	}
	if alreadyBound {
		if token == "" {
			return "", ErrAppleChainConflict
		}
		return token, tx.Commit(ctx)
	}
	// New chains must be compatible before a remote assignment can occur.
	snapshot, e := appleSnapshot(ctx, tx, brand, r.Now(), r.appleEnvironment())
	if e != nil {
		return "", e
	}
	var mp bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM suscripciones_marca WHERE marca_id=$1 AND estado<>'CANCELLED')`, brand).Scan(&mp); e != nil {
		return "", e
	}
	if mp {
		return "", ErrBillingProviderConflict
	}
	if p.ProgramType != snapshot.BrandProgram {
		return "", ErrAppleProductConflict
	}
	if p.Capacity < snapshot.State.ActiveBranches {
		return "", ErrAppleCapacity
	}
	if e = requireAppleReferralOffer(ctx, tx, brand, t); e != nil {
		return "", e
	}
	if token == "" {
		// Never synthesize an account matching a token already assigned by Apple.
		if t.AppAccountToken != "" {
			return "", ErrAppleChainConflict
		}
		token = uuid.NewString()
		if _, e = tx.Exec(ctx, `INSERT INTO apple_billing_accounts(marca_id,app_account_token) VALUES($1,$2)`, brand, token); e != nil {
			return "", e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO apple_claim_reservations(environment,original_transaction_id,marca_id,app_account_token) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, t.Environment, t.OriginalTransactionID, brand, token)
	if e != nil {
		return "", e
	}
	var reserved int64
	var reservedToken string
	if e = tx.QueryRow(ctx, `SELECT marca_id,app_account_token::text FROM apple_claim_reservations WHERE environment=$1 AND original_transaction_id=$2 FOR UPDATE`, t.Environment, t.OriginalTransactionID).Scan(&reserved, &reservedToken); e != nil {
		return "", e
	}
	if reserved != brand || reservedToken != token {
		return "", ErrAppleChainConflict
	}
	return token, tx.Commit(ctx)
}

func (r *Repository) SaveAppleTransaction(ctx context.Context, actor, brand int64, t model.AppleTransaction, renewal *model.AppleRenewal) error {
	if t.Environment != r.appleEnvironment() {
		return ErrAppleEnvironmentConflict
	}
	p, ok := applebilling.Product(t.ProductID)
	if !ok {
		return ErrInvalidRequest
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if actor > 0 {
		e = lockAppleOwner(ctx, tx, actor, brand)
	} else {
		var id int64
		e = tx.QueryRow(ctx, `SELECT id FROM marcas WHERE id=$1 FOR UPDATE`, brand).Scan(&id)
	}
	if e != nil {
		return e
	}
	if e = lockAppleChain(ctx, tx, t); e != nil {
		return e
	}
	var token string
	var live bool
	e = tx.QueryRow(ctx, `SELECT app_account_token::text,EXISTS(SELECT 1 FROM marcas WHERE id=$1 AND activo AND deleted_at IS NULL) FROM apple_billing_accounts WHERE marca_id=$1`, brand).Scan(&token, &live)
	if e != nil {
		return e
	}
	if t.AppAccountToken != token {
		return ErrAppleChainConflict
	}
	var mp bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM suscripciones_marca WHERE marca_id=$1 AND estado<>'CANCELLED')`, brand).Scan(&mp); e != nil {
		return e
	}
	if mp {
		return ErrBillingProviderConflict
	}
	var reservedBrand int64
	reservationErr := tx.QueryRow(ctx, `SELECT marca_id FROM apple_claim_reservations WHERE environment=$1 AND original_transaction_id=$2`, t.Environment, t.OriginalTransactionID).Scan(&reservedBrand)
	if reservationErr != nil && !errors.Is(reservationErr, pgx.ErrNoRows) {
		return reservationErr
	}
	if reservationErr == nil && reservedBrand != brand {
		return ErrAppleChainConflict
	}
	var alreadyBound bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM apple_subscription_chains WHERE environment=$1 AND original_transaction_id=$2)`, t.Environment, t.OriginalTransactionID).Scan(&alreadyBound); e != nil {
		return e
	}
	if !alreadyBound {
		if e = requireAppleReferralOffer(ctx, tx, brand, t); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO apple_subscription_chains(environment,original_transaction_id,marca_id) VALUES($1,$2,$3) ON CONFLICT(environment,original_transaction_id) DO NOTHING`, t.Environment, t.OriginalTransactionID, brand)
	if IsUniqueViolation(e) {
		return ErrAppleChainConflict
	}
	if e != nil {
		return e
	}
	var bound int64
	e = tx.QueryRow(ctx, `SELECT marca_id FROM apple_subscription_chains WHERE environment=$1 AND original_transaction_id=$2 FOR UPDATE`, t.Environment, t.OriginalTransactionID).Scan(&bound)
	if e != nil {
		return e
	}
	if bound != brand {
		return ErrAppleChainConflict
	}
	payload, e := json.Marshal(t)
	if e != nil {
		return e
	}
	var revoked *time.Time
	if t.RevocationDate > 0 {
		date := time.UnixMilli(t.RevocationDate)
		revoked = &date
	}
	receiptCommand, e := tx.Exec(ctx, `INSERT INTO apple_transactions(environment,transaction_id,original_transaction_id,product_id,program_type,capacity,purchase_at,expires_at,signed_at,revoked_at,upgraded,currency,price_milliunits,offer_id,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),$13,NULLIF($14,''),$15) ON CONFLICT(environment,transaction_id) DO UPDATE SET expires_at=EXCLUDED.expires_at,signed_at=EXCLUDED.signed_at,revoked_at=CASE WHEN EXCLUDED.revoked_at IS NOT NULL THEN EXCLUDED.revoked_at WHEN $16 THEN NULL ELSE apple_transactions.revoked_at END,upgraded=EXCLUDED.upgraded,payload=EXCLUDED.payload WHERE EXCLUDED.signed_at>=apple_transactions.signed_at AND apple_transactions.original_transaction_id=EXCLUDED.original_transaction_id`, t.Environment, t.TransactionID, t.OriginalTransactionID, t.ProductID, p.ProgramType, p.Capacity, time.UnixMilli(t.PurchaseDate), time.UnixMilli(t.ExpiresDate), time.UnixMilli(t.SignedDate), revoked, t.IsUpgraded, t.Currency, t.Price, t.OfferIdentifier, payload, t.RevocationReversed)
	if e != nil {
		return e
	}
	if renewal != nil {
		if renewal.OriginalTransactionID != t.OriginalTransactionID || renewal.Environment != t.Environment {
			return ErrInvalidRequest
		}
		var grace *time.Time
		if renewal.GracePeriodExpiresDate > 0 {
			v := time.UnixMilli(renewal.GracePeriodExpiresDate)
			grace = &v
		}
		_, e = tx.Exec(ctx, `INSERT INTO apple_renewal_state(environment,original_transaction_id,auto_renew,pending_product_id,apple_grace_ends_at,signed_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6) ON CONFLICT(environment,original_transaction_id) DO UPDATE SET auto_renew=EXCLUDED.auto_renew,pending_product_id=EXCLUDED.pending_product_id,apple_grace_ends_at=EXCLUDED.apple_grace_ends_at,signed_at=EXCLUDED.signed_at WHERE EXCLUDED.signed_at>=apple_renewal_state.signed_at`, t.Environment, t.OriginalTransactionID, renewal.AutoRenewStatus == 1, renewal.AutoRenewProductID, grace, time.UnixMilli(renewal.SignedDate))
		if e != nil {
			return e
		}
	}
	// Notifications for a deleted brand retain billing provenance but cannot resurrect it.
	if live {
		if _, e = tx.Exec(ctx, `UPDATE apple_billing_accounts SET mode='APPLE',active_environment=$2 WHERE marca_id=$1`, brand, t.Environment); e != nil {
			return e
		}
	}
	if receiptCommand.RowsAffected() > 0 {
		if e = saveAppleReferral(ctx, tx, brand, t); e != nil {
			return e
		}
	} else {
		var original string
		if e = tx.QueryRow(ctx, `SELECT original_transaction_id FROM apple_transactions WHERE environment=$1 AND transaction_id=$2`, t.Environment, t.TransactionID).Scan(&original); e != nil {
			return e
		}
		if original != t.OriginalTransactionID {
			return ErrAppleChainConflict
		}
	}
	return tx.Commit(ctx)
}
func saveAppleReferral(ctx context.Context, tx pgx.Tx, brand int64, t model.AppleTransaction) error {
	if t.RevocationDate > 0 {
		_, e := tx.Exec(ctx, `UPDATE apple_referral_charges SET status=CASE WHEN status='SETTLED' THEN 'RECOVERY_DUE' ELSE 'VOID' END WHERE environment=$1 AND transaction_id=$2`, t.Environment, t.TransactionID)
		return e
	}
	if t.RevocationReversed {
		if _, e := tx.Exec(ctx, `UPDATE apple_referral_charges SET status=CASE WHEN status='VOID' THEN 'PENDING' WHEN status='RECOVERY_DUE' THEN 'SETTLED' ELSE status END WHERE environment=$1 AND transaction_id=$2`, t.Environment, t.TransactionID); e != nil {
			return e
		}
	}
	if t.Price <= 0 || len(t.Currency) != 3 {
		return nil
	}
	var source string
	var influencer, sourceBrand *int64
	var bps, charges int
	e := tx.QueryRow(ctx, `SELECT source_kind,influencer_id,source_brand_id,reward_bps,reward_charges FROM referral_attributions WHERE brand_id=$1`, brand).Scan(&source, &influencer, &sourceBrand, &bps, &charges)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var exists bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM apple_referral_charges WHERE environment=$1 AND transaction_id=$2)`, t.Environment, t.TransactionID).Scan(&exists)
	if e != nil || exists {
		return e
	}
	var index int
	e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM apple_referral_charges WHERE marca_id=$1 AND environment=$2)+(SELECT count(*) FROM referral_charges WHERE brand_id=$1)+1`, brand, t.Environment).Scan(&index)
	if e != nil {
		return e
	}
	reward := referralRewardMinor(t.Price, index, bps, charges)
	_, e = tx.Exec(ctx, `INSERT INTO apple_referral_charges(environment,transaction_id,marca_id,currency,amount_milliunits,reward_milliunits,paid_index,source_kind,influencer_id,source_brand_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, t.Environment, t.TransactionID, brand, t.Currency, t.Price, reward, index, source, influencer, sourceBrand)
	if e != nil {
		return e
	}
	// Arrival order is not payment order. Recompute the Apple sequence from signed
	// purchase dates; a delayed old renewal cannot consume the wrong reward window.
	_, e = tx.Exec(ctx, `WITH ranked AS (SELECT c.environment,c.transaction_id,((SELECT count(*) FROM referral_charges WHERE brand_id=$1)+row_number() OVER(ORDER BY t.purchase_at,t.transaction_id))::integer AS idx FROM apple_referral_charges c JOIN apple_transactions t USING(environment,transaction_id) WHERE c.marca_id=$1 AND c.environment=$2), amounts AS (SELECT r.*,CASE WHEN r.idx<=$4 THEN (c.amount_milliunits/10000)*$3::bigint+((c.amount_milliunits%10000)*$3::bigint+5000)/10000 ELSE 0 END AS reward FROM ranked r JOIN apple_referral_charges c USING(environment,transaction_id)) UPDATE apple_referral_charges c SET paid_index=a.idx,reward_milliunits=a.reward,status=CASE WHEN c.status='SETTLED' AND c.reward_milliunits<>a.reward THEN 'RECOVERY_DUE' ELSE c.status END FROM amounts a WHERE c.environment=a.environment AND c.transaction_id=a.transaction_id`, brand, t.Environment, bps, charges)
	return e
}
func (r *Repository) AppleBrandForTransaction(ctx context.Context, t model.AppleTransaction) (int64, error) {
	var brand int64
	e := r.Pool.QueryRow(ctx, `SELECT a.marca_id FROM apple_billing_accounts a JOIN apple_subscription_chains c ON c.marca_id=a.marca_id WHERE c.environment=$1 AND c.original_transaction_id=$2 AND ($3='' OR a.app_account_token::text=$3)`, t.Environment, t.OriginalTransactionID, t.AppAccountToken).Scan(&brand)
	if errors.Is(e, pgx.ErrNoRows) {
		e = r.Pool.QueryRow(ctx, `SELECT marca_id FROM apple_billing_accounts WHERE app_account_token::text=$1`, t.AppAccountToken).Scan(&brand)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, ErrAppleIntentRequired
	}
	return brand, e
}

func requireAppleReferralOffer(ctx context.Context, tx pgx.Tx, brand int64, t model.AppleTransaction) error {
	var campaign int64
	var bps, charges, paid int
	e := tx.QueryRow(ctx, `SELECT campaign_id,discount_bps,discount_charges,(SELECT count(*) FROM apple_referral_charges c WHERE c.marca_id=$1 AND c.environment=$2)+(SELECT count(*) FROM referral_charges c WHERE c.brand_id=$1) FROM referral_attributions WHERE brand_id=$1`, brand, t.Environment).Scan(&campaign, &bps, &charges, &paid)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if bps == 0 || paid >= charges {
		return nil
	}
	var offer string
	e = tx.QueryRow(ctx, `SELECT offer_id FROM apple_referral_offers WHERE campaign_id=$1 AND product_id=$2 AND environment=$3 AND enabled AND discount_bps=$4 AND discount_charges=$5`, campaign, t.ProductID, t.Environment, bps, charges-paid).Scan(&offer)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrAppleOfferUnavailable
	}
	if e != nil {
		return e
	}
	if t.OfferIdentifier != offer || t.OfferType != 3 {
		return ErrAppleOfferUnavailable
	}
	return nil
}

func (r *Repository) appleEnvironment() string {
	if r.AppleBillingEnvironment == "" {
		return "Production"
	}
	return r.AppleBillingEnvironment
}

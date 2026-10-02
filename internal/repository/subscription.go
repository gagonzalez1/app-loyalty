package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

type BillingContext struct {
	PayerEmail          string
	ActiveBranches      int64
	ProgramType         string
	TrialStartedAt      *time.Time
	TrialStartEstimated bool
}
type SubscriptionRecord struct {
	Subscription                  model.Subscription
	ProviderID, ExternalReference string
	TrialMonths                   int
}

// discountedMinor rounds one branch's monthly price to the nearest minor unit.
func discountedMinor(full int64, discountBPS int) int64 {
	payBPS := int64(10000 - discountBPS)
	return (full/10000)*payBPS + ((full%10000)*payBPS+5000)/10000
}

func referralRewardMinor(collected int64, paidIndex, rewardBPS, rewardCharges int) int64 {
	if collected < 1 || paidIndex < 1 || paidIndex > rewardCharges || rewardBPS < 1 {
		return 0
	}
	return (collected/10000)*int64(rewardBPS) + ((collected%10000)*int64(rewardBPS)+5000)/10000
}

func (r *Repository) ReferralCheckoutPrice(ctx context.Context, brandID, fullUnitPrice int64) (int64, int, error) {
	var discountBPS, discountCharges, paidCount int
	err := r.Pool.QueryRow(ctx, `SELECT a.discount_bps,a.discount_charges,(SELECT count(*) FROM referral_charges c WHERE c.brand_id=$1) FROM referral_attributions a WHERE a.brand_id=$1`, brandID).Scan(&discountBPS, &discountCharges, &paidCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return fullUnitPrice, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	remaining := max(discountCharges-paidCount, 0)
	if remaining == 0 {
		return fullUnitPrice, 0, nil
	}
	return discountedMinor(fullUnitPrice, discountBPS), remaining, nil
}

// The caller holds the brand row lock, so a checkout cannot race a branch change or deletion.
func requireNoActiveSubscription(ctx context.Context, tx pgx.Tx, brandID int64) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM suscripciones_marca WHERE marca_id=$1 AND estado<>'CANCELLED')`, brandID).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrSubscriptionChangeRequired
	}
	return nil
}

func (r *Repository) BillingContext(ctx context.Context, actorID, brandID int64) (BillingContext, error) {
	var out BillingContext
	err := r.Pool.QueryRow(ctx, `SELECT u.email::text,(SELECT count(*) FROM sucursales s WHERE s.marca_id=$2 AND s.activo AND s.deleted_at IS NULL),p.tipo,m.trial_started_at,m.trial_start_estimated FROM membresias_marca mm JOIN usuarios u ON u.id=mm.usuario_id JOIN marcas m ON m.id=mm.marca_id AND m.activo JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo AND mm.rol='PROPIETARIO'`, actorID, brandID).Scan(&out.PayerEmail, &out.ActiveBranches, &out.ProgramType, &out.TrialStartedAt, &out.TrialStartEstimated)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrForbidden
	}
	if err != nil {
		return out, err
	}
	if out.ActiveBranches < 1 {
		return out, ErrInvalidRequest
	}
	return out, nil
}

func (r *Repository) GetSubscriptionRecord(ctx context.Context, brandID int64) (SubscriptionRecord, error) {
	var out SubscriptionRecord
	s := &out.Subscription
	err := r.Pool.QueryRow(ctx, `SELECT marca_id,proveedor,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at,trial_ends_at,checkout_rejected,COALESCE(proveedor_suscripcion_id,''),referencia_externa,trial_months,COALESCE(full_unit_price_minor,precio_sucursal_minor)*cantidad_sucursales,COALESCE((SELECT greatest(a.discount_charges-count(c.provider_invoice_id),0) FROM referral_attributions a LEFT JOIN referral_charges c ON c.brand_id=a.brand_id WHERE a.brand_id=suscripciones_marca.marca_id GROUP BY a.discount_charges),0) FROM suscripciones_marca WHERE marca_id=$1`, brandID).Scan(&s.BrandID, &s.Provider, &s.Status, &s.Currency, &s.UnitAmountCents, &s.ActiveBranches, &s.MonthlyAmountCents, &s.CheckoutURL, &s.NextPaymentDate, &s.UpdatedAt, &s.TrialEndsAt, &s.CheckoutRejected, &out.ProviderID, &out.ExternalReference, &out.TrialMonths, &s.FullMonthlyAmountCents, &s.DiscountRemainingCharges)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	s.TrialAvailable = s.Status == "CREATING" && out.TrialMonths > 0
	return out, err
}

// ReserveSubscriptionCheckout commits the idempotency key before the external API call.
// A CREATING row keeps its reference for reconciliation after an uncertain provider call.
func (r *Repository) ReserveSubscriptionCheckout(ctx context.Context, actorID, brandID int64, external string, stampPrice, pointsPrice int64) (BillingContext, SubscriptionRecord, error) {
	var billing BillingContext
	var record SubscriptionRecord
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return billing, record, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT u.email::text,p.tipo,m.trial_started_at,m.trial_start_estimated,(SELECT count(*) FROM sucursales s WHERE s.marca_id=$2 AND s.activo AND s.deleted_at IS NULL) FROM marcas m JOIN membresias_marca mm ON mm.marca_id=m.id AND mm.usuario_id=$1 AND mm.activo AND mm.rol='PROPIETARIO' JOIN usuarios u ON u.id=mm.usuario_id JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo WHERE m.id=$2 AND m.activo FOR UPDATE OF m`, actorID, brandID).Scan(&billing.PayerEmail, &billing.ProgramType, &billing.TrialStartedAt, &billing.TrialStartEstimated, &billing.ActiveBranches)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing, record, ErrForbidden
	}
	if err != nil {
		return billing, record, err
	}
	if billing.ActiveBranches < 1 {
		return billing, record, ErrInvalidRequest
	}
	unitPrice := stampPrice
	if billing.ProgramType == "PUNTOS" {
		unitPrice = pointsPrice
	} else if billing.ProgramType != "SELLOS" {
		return billing, record, ErrInvalidRequest
	}
	if unitPrice < 1 {
		return billing, record, ErrInvalidRequest
	}
	fullUnitPrice := unitPrice
	var configuredPrice int64
	priceErr := tx.QueryRow(ctx, `SELECT unit_price_minor FROM subscription_prices WHERE program_type=$1`, billing.ProgramType).Scan(&configuredPrice)
	if priceErr == nil {
		unitPrice = configuredPrice
		fullUnitPrice = configuredPrice
	} else if !errors.Is(priceErr, pgx.ErrNoRows) {
		return billing, record, priceErr
	}
	if unitPrice > MaxSubscriptionAmountMinor/billing.ActiveBranches {
		return billing, record, ErrInvalidRequest
	}
	var discountBPS, discountCharges, paidCount int
	err = tx.QueryRow(ctx, `SELECT a.discount_bps,a.discount_charges,(SELECT count(*) FROM referral_charges c WHERE c.brand_id=$1) FROM referral_attributions a WHERE a.brand_id=$1`, brandID).Scan(&discountBPS, &discountCharges, &paidCount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return billing, record, err
	}
	if err == nil && paidCount < discountCharges {
		unitPrice = discountedMinor(unitPrice, discountBPS)
	}
	if unitPrice < 1 {
		return billing, record, ErrInvalidRequest
	}
	var storedFullUnit int64
	err = tx.QueryRow(ctx, `SELECT estado,COALESCE(proveedor_suscripcion_id,''),referencia_externa,trial_months,marca_id,proveedor,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at,trial_ends_at,COALESCE(full_unit_price_minor,precio_sucursal_minor) FROM suscripciones_marca WHERE marca_id=$1 FOR UPDATE`, brandID).Scan(&record.Subscription.Status, &record.ProviderID, &record.ExternalReference, &record.TrialMonths, &record.Subscription.BrandID, &record.Subscription.Provider, &record.Subscription.Currency, &record.Subscription.UnitAmountCents, &record.Subscription.ActiveBranches, &record.Subscription.MonthlyAmountCents, &record.Subscription.CheckoutURL, &record.Subscription.NextPaymentDate, &record.Subscription.UpdatedAt, &record.Subscription.TrialEndsAt, &storedFullUnit)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return billing, record, err
	}
	if err == nil {
		record.Subscription.FullMonthlyAmountCents = storedFullUnit * record.Subscription.ActiveBranches
		record.Subscription.DiscountRemainingCharges = max(discountCharges-paidCount, 0)
		if record.Subscription.Status == "CREATING" {
			record.Subscription.TrialAvailable = record.TrialMonths > 0
			return billing, record, tx.Commit(ctx)
		}
		if record.ExternalReference == external {
			return billing, record, tx.Commit(ctx)
		}
		if record.Subscription.Status != "CANCELLED" {
			return billing, record, ErrConflict
		}
	}
	trialMonths := 0
	var trialEnd *time.Time
	if billing.TrialStartedAt != nil {
		end := TrialEnd(*billing.TrialStartedAt)
		trialEnd = &end
		if end.After(r.Now()) {
			trialMonths = 1
		}
	}
	query := `INSERT INTO suscripciones_marca(marca_id,proveedor,proveedor_suscripcion_id,referencia_externa,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,trial_months,full_unit_price_minor,program_type,price_valid_from,trial_ends_at) VALUES($1,'MERCADO_PAGO',NULL,$2,'CREATING','ARS',$3,$4,$3::bigint*$4::bigint,$5,$6,$7,now(),$8) ON CONFLICT(marca_id) DO UPDATE SET proveedor_suscripcion_id=NULL,referencia_externa=EXCLUDED.referencia_externa,estado='CREATING',precio_sucursal_minor=EXCLUDED.precio_sucursal_minor,cantidad_sucursales=EXCLUDED.cantidad_sucursales,importe_mensual_minor=EXCLUDED.importe_mensual_minor,checkout_rejected=false,trial_months=EXCLUDED.trial_months,trial_ends_at=EXCLUDED.trial_ends_at,full_unit_price_minor=EXCLUDED.full_unit_price_minor,program_type=EXCLUDED.program_type,price_valid_from=now(),price_transitioned_at=NULL,provider_call_started_at=NULL,checkout_url=NULL,proximo_cobro_at=NULL,updated_at=now() RETURNING marca_id,proveedor,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at,referencia_externa,trial_months,trial_ends_at`
	err = tx.QueryRow(ctx, query, brandID, external, unitPrice, billing.ActiveBranches, trialMonths, fullUnitPrice, billing.ProgramType, trialEnd).Scan(&record.Subscription.BrandID, &record.Subscription.Provider, &record.Subscription.Status, &record.Subscription.Currency, &record.Subscription.UnitAmountCents, &record.Subscription.ActiveBranches, &record.Subscription.MonthlyAmountCents, &record.Subscription.CheckoutURL, &record.Subscription.NextPaymentDate, &record.Subscription.UpdatedAt, &record.ExternalReference, &record.TrialMonths, &record.Subscription.TrialEndsAt)
	if err != nil {
		return billing, record, err
	}
	record.Subscription.TrialAvailable = record.TrialMonths > 0
	record.Subscription.FullMonthlyAmountCents = fullUnitPrice * billing.ActiveBranches
	record.Subscription.DiscountRemainingCharges = max(discountCharges-paidCount, 0)
	return billing, record, tx.Commit(ctx)
}

// RecordReferralInvoice serializes each brand's paid sequence with checkout.
// The provider price is advanced before committing the third discounted charge.
// Retrying an uncertain PUT with the same target amount is safe and required.
func (r *Repository) RecordReferralInvoice(ctx context.Context, notificationID string, invoice model.BillingInvoice, payment model.BillingPayment, advance func(context.Context, string, int64, string) error) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var brandID, fullUnit, unit, branches int64
	err = tx.QueryRow(ctx, `SELECT marca_id,COALESCE(full_unit_price_minor,precio_sucursal_minor),precio_sucursal_minor,cantidad_sucursales FROM suscripciones_marca WHERE proveedor_suscripcion_id=$1 AND moneda='ARS' FOR UPDATE`, invoice.SubscriptionID).Scan(&brandID, &fullUnit, &unit, &branches)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if payment.Status == "refunded" || payment.Status == "charged_back" || payment.RefundedMinor > 0 {
		if _, err = tx.Exec(ctx, `UPDATE referral_merchant_credit_allocations SET status='RECOVERY_DUE',recovery_due_at=now() WHERE provider_payment_id=$1 AND status='RECORDED'`, payment.ID); err != nil {
			return err
		}
	}
	var discountBPS, discountCharges, rewardBPS, rewardCharges int
	var sourceKind string
	var influencerID, sourceBrandID *int64
	err = tx.QueryRow(ctx, `SELECT discount_bps,discount_charges,reward_bps,reward_charges,source_kind,influencer_id,source_brand_id FROM referral_attributions WHERE brand_id=$1`, brandID).Scan(&discountBPS, &discountCharges, &rewardBPS, &rewardCharges, &sourceKind, &influencerID, &sourceBrandID)
	// Every verified subscription payment belongs in the charge ledger. A brand
	// without attribution pays full price and generates no referral reward.
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var oldStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM referral_charges WHERE provider_invoice_id=$1 FOR UPDATE`, invoice.ID).Scan(&oldStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	refunded := payment.Status == "refunded" || payment.Status == "charged_back" || payment.RefundedMinor > 0
	if err == nil {
		if refunded && oldStatus == "APPROVED" {
			if _, err = tx.Exec(ctx, `UPDATE referral_merchant_credit_allocations SET status='RECOVERY_DUE',recovery_due_at=now() WHERE provider_payment_id=$1 AND status='RECORDED'`, payment.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE referral_charges SET status='REFUNDED',updated_at=now() WHERE provider_invoice_id=$1`, invoice.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE referral_rewards SET recovery_due_at=CASE WHEN status='SETTLED' THEN now() ELSE recovery_due_at END,status=CASE WHEN status='SETTLED' THEN 'RECOVERY_DUE' ELSE 'VOID' END WHERE provider_invoice_id=$1 AND status IN ('PENDING','SETTLED')`, invoice.ID); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	if refunded || payment.Status != "approved" || payment.AmountMinor < 1 {
		return tx.Commit(ctx)
	}
	var paidCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM referral_charges WHERE brand_id=$1`, brandID).Scan(&paidCount); err != nil {
		return err
	}
	index := paidCount + 1
	expectedUnit := fullUnit
	if index <= discountCharges {
		expectedUnit = discountedMinor(fullUnit, discountBPS)
	}
	fullAmountMinor := fullUnit * branches
	validAmount := invoice.AmountMinor == expectedUnit*branches
	// An invoice already issued before a bulk price update retains its old amount.
	// Only the provider's authenticated creation timestamp and exact historical
	// price can authorize that exception; an undated mismatch remains an error.
	if !validAmount && !invoice.CreatedAt.IsZero() {
		historyErr := tx.QueryRow(ctx, `SELECT full_unit_price_minor*branches FROM subscription_price_history
 WHERE provider_subscription_id=$1 AND $2>=valid_from AND $2<valid_until AND unit_price_minor*branches=$3
 ORDER BY valid_until DESC LIMIT 1`, invoice.SubscriptionID, invoice.CreatedAt, invoice.AmountMinor).Scan(&fullAmountMinor)
		if historyErr == nil {
			validAmount = true
		} else if !errors.Is(historyErr, pgx.ErrNoRows) {
			return historyErr
		}
	}
	if !validAmount && !invoice.CreatedAt.IsZero() {
		historyErr := tx.QueryRow(ctx, `SELECT h.full_unit_price_minor*h.branches FROM subscription_quantity_history h JOIN branch_operations o ON o.id=h.operation_id JOIN branch_quotes q ON q.id=o.quote_id WHERE q.subscription_snapshot->>'provider_id'=$1 AND $2>=h.valid_from AND $2<h.valid_until AND h.unit_price_minor*h.branches=$3 ORDER BY h.valid_until DESC LIMIT 1`, invoice.SubscriptionID, invoice.CreatedAt, invoice.AmountMinor).Scan(&fullAmountMinor)
		if historyErr == nil {
			validAmount = true
		} else if !errors.Is(historyErr, pgx.ErrNoRows) {
			return historyErr
		}
	}
	if invoice.AmountMinor != payment.AmountMinor || !validAmount {
		return fmt.Errorf("referral invoice amount does not match price snapshot")
	}
	if index == discountCharges && discountCharges > 0 && expectedUnit != fullUnit {
		if err = requireNoPendingBranch(ctx, tx, brandID); err != nil {
			return err
		}
		fullAmount := fullUnit * branches
		if err = advance(ctx, invoice.SubscriptionID, fullAmount, "referral-full-price:"+invoice.SubscriptionID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE suscripciones_marca SET precio_sucursal_minor=$2,importe_mensual_minor=$3,price_transitioned_at=now(),updated_at=now() WHERE marca_id=$1`, brandID, fullUnit, fullAmount); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO referral_charges(provider_invoice_id,brand_id,provider_payment_id,amount_minor,full_amount_minor,currency,status,paid_index) VALUES($1,$2,$3,$4,$5,'ARS','APPROVED',$6)`, invoice.ID, brandID, payment.ID, payment.AmountMinor, fullAmountMinor, index); err != nil {
		return normalize(err)
	}
	if reward := referralRewardMinor(payment.AmountMinor, index, rewardBPS, rewardCharges); reward > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO referral_rewards(provider_invoice_id,brand_id,source_kind,influencer_id,source_brand_id,amount_minor,status) VALUES($1,$2,$3,$4,$5,$6,'PENDING')`, invoice.ID, brandID, sourceKind, influencerID, sourceBrandID, reward); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO eventos_mercado_pago(notification_id,resource_id,topic) VALUES($1,$2,'subscription_authorized_payment') ON CONFLICT DO NOTHING`, notificationID, invoice.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) ReferralInvoiceForPayment(ctx context.Context, paymentID string) (string, error) {
	var invoiceID string
	err := r.Pool.QueryRow(ctx, `SELECT provider_invoice_id FROM referral_charges WHERE provider_payment_id=$1 UNION SELECT provider_invoice_id FROM referral_merchant_credit_allocations WHERE provider_payment_id=$1 LIMIT 1`, paymentID).Scan(&invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return invoiceID, err
}

func (r *Repository) ReverseReferralPayment(ctx context.Context, notificationID string, payment model.BillingPayment) error {
	if payment.Status != "refunded" && payment.Status != "charged_back" && payment.RefundedMinor == 0 {
		return nil
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE referral_merchant_credit_allocations SET status='RECOVERY_DUE',recovery_due_at=now() WHERE provider_payment_id=$1 AND status='RECORDED'`, payment.ID); err != nil {
		return err
	}
	var invoiceID string
	err = tx.QueryRow(ctx, `SELECT provider_invoice_id FROM referral_charges WHERE provider_payment_id=$1 FOR UPDATE`, payment.ID).Scan(&invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE referral_charges SET status='REFUNDED',updated_at=now() WHERE provider_invoice_id=$1 AND status='APPROVED'`, invoiceID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE referral_rewards SET recovery_due_at=CASE WHEN status='SETTLED' THEN now() ELSE recovery_due_at END,status=CASE WHEN status='SETTLED' THEN 'RECOVERY_DUE' ELSE 'VOID' END WHERE provider_invoice_id=$1 AND status IN ('PENDING','SETTLED')`, invoiceID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO eventos_mercado_pago(notification_id,resource_id,topic) VALUES($1,$2,'payment') ON CONFLICT DO NOTHING`, notificationID, payment.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ClaimSubscriptionProviderCall allows at most one POST to /preapproval. The
// provider does not document idempotency for that endpoint, so an uncertain
// result must be reconciled via webhook or operator review before another POST.
func (r *Repository) ClaimSubscriptionProviderCall(ctx context.Context, brandID int64, external string) (bool, error) {
	tag, err := r.Pool.Exec(ctx, `UPDATE suscripciones_marca SET provider_call_started_at=now(),updated_at=now() WHERE marca_id=$1 AND referencia_externa=$2 AND estado='CREATING' AND provider_call_started_at IS NULL`, brandID, external)
	return tag.RowsAffected() == 1, err
}

func (r *Repository) SaveSubscriptionCheckout(ctx context.Context, brandID int64, provider model.BillingSubscriptionResult) (model.Subscription, error) {
	status, ok := providerStatus(provider.Status)
	if !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	var out model.Subscription
	err := r.Pool.QueryRow(ctx, `UPDATE suscripciones_marca SET proveedor_suscripcion_id=$2,estado=$3,checkout_url=NULLIF($4,''),proximo_cobro_at=$5,updated_at=now() WHERE marca_id=$1 AND referencia_externa=$6 AND estado='CREATING' AND provider_call_started_at IS NOT NULL RETURNING marca_id,proveedor,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at`, brandID, provider.ID, status, provider.CheckoutURL, provider.NextPaymentDate, provider.ExternalReference).Scan(&out.BrandID, &out.Provider, &out.Status, &out.Currency, &out.UnitAmountCents, &out.ActiveBranches, &out.MonthlyAmountCents, &out.CheckoutURL, &out.NextPaymentDate, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrConflict
	}
	return out, normalize(err)
}

func (r *Repository) RecordSubscriptionWebhook(ctx context.Context, notificationID, topic string, provider model.BillingSubscriptionResult) error {
	return r.recordSubscriptionWebhook(ctx, notificationID, topic, provider, false)
}

func (r *Repository) RecordSubscriptionWebhookWithConfirmation(ctx context.Context, notificationID, topic string, provider model.BillingSubscriptionResult) error {
	return r.recordSubscriptionWebhook(ctx, notificationID, topic, provider, true)
}

func (r *Repository) recordSubscriptionWebhook(ctx context.Context, notificationID, topic string, provider model.BillingSubscriptionResult, notify bool) error {
	status, ok := providerStatus(provider.Status)
	if !ok {
		return ErrInvalidRequest
	}
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO eventos_mercado_pago(notification_id,resource_id,topic) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, notificationID, provider.ID, topic)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	// A webhook can have read the provider before account deletion canceled it.
	// Never let that delayed response reactivate a closed brand's subscription.
	tag, err = tx.Exec(ctx, `UPDATE suscripciones_marca s SET proveedor_suscripcion_id=COALESCE(s.proveedor_suscripcion_id,$1),estado=CASE WHEN s.estado<>'CANCELLED' AND m.activo AND m.deleted_at IS NULL THEN $2 ELSE 'CANCELLED' END,checkout_url=CASE WHEN s.estado<>'CANCELLED' AND $2<>'CANCELLED' AND m.activo AND m.deleted_at IS NULL THEN COALESCE(NULLIF($3,''),s.checkout_url) ELSE NULL END,proximo_cobro_at=CASE WHEN s.estado<>'CANCELLED' AND $2<>'CANCELLED' AND m.activo AND m.deleted_at IS NULL THEN $4::timestamptz ELSE NULL END,updated_at=now() FROM marcas m WHERE m.id=s.marca_id AND s.referencia_externa=$5 AND (s.proveedor_suscripcion_id=$1 OR (s.estado='CREATING' AND s.proveedor_suscripcion_id IS NULL AND s.provider_call_started_at IS NOT NULL))`, provider.ID, status, provider.CheckoutURL, provider.NextPaymentDate, provider.ExternalReference)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	if notify && status == "AUTHORIZED" {
		var brandID int64
		if err = tx.QueryRow(ctx, `SELECT marca_id FROM suscripciones_marca WHERE proveedor_suscripcion_id=$1`, provider.ID).Scan(&brandID); err != nil {
			return err
		}
		if err = r.enqueueSubscriptionConfirmation(ctx, tx, brandID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) UpdateSubscriptionFromProvider(ctx context.Context, provider model.BillingSubscriptionResult) (model.Subscription, error) {
	status, ok := providerStatus(provider.Status)
	if !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	var out model.Subscription
	err := r.Pool.QueryRow(ctx, `UPDATE suscripciones_marca s SET estado=CASE WHEN s.estado<>'CANCELLED' AND m.activo AND m.deleted_at IS NULL THEN $2 ELSE 'CANCELLED' END,checkout_url=CASE WHEN s.estado<>'CANCELLED' AND $2<>'CANCELLED' AND m.activo AND m.deleted_at IS NULL THEN COALESCE(NULLIF($3,''),s.checkout_url) ELSE NULL END,proximo_cobro_at=CASE WHEN s.estado<>'CANCELLED' AND $2<>'CANCELLED' AND m.activo AND m.deleted_at IS NULL THEN $4::timestamptz ELSE NULL END,updated_at=now() FROM marcas m WHERE m.id=s.marca_id AND s.proveedor_suscripcion_id=$1 AND s.referencia_externa=$5 RETURNING s.marca_id,s.proveedor,s.estado,s.moneda,s.precio_sucursal_minor,s.cantidad_sucursales,s.importe_mensual_minor,COALESCE(s.checkout_url,''),s.proximo_cobro_at,s.updated_at`, provider.ID, status, provider.CheckoutURL, provider.NextPaymentDate, provider.ExternalReference).Scan(&out.BrandID, &out.Provider, &out.Status, &out.Currency, &out.UnitAmountCents, &out.ActiveBranches, &out.MonthlyAmountCents, &out.CheckoutURL, &out.NextPaymentDate, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func providerStatus(value string) (string, bool) {
	switch value {
	case "pending":
		return "PENDING", true
	case "authorized":
		return "AUTHORIZED", true
	case "paused":
		return "PAUSED", true
	case "cancelled", "canceled":
		return "CANCELLED", true
	default:
		return "", false
	}
}

// TrialEnd uses an Argentina calendar month, clamping the day to the next month's end.
func TrialEnd(start time.Time) time.Time {
	zone := time.FixedZone("Argentina", -3*60*60)
	start = start.In(zone)
	next := time.Date(start.Year(), start.Month()+1, 1, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), zone)
	last := time.Date(next.Year(), next.Month()+1, 0, 0, 0, 0, 0, zone).Day()
	day := min(start.Day(), last)
	return time.Date(next.Year(), next.Month(), day, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), zone)
}

// RejectSubscriptionCheckout releases only a confirmed rejection; uncertain calls stay reserved.
func (r *Repository) RejectSubscriptionCheckout(ctx context.Context, brandID int64, reference string) error {
	_, err := r.Pool.Exec(ctx, `UPDATE suscripciones_marca SET estado='CANCELLED',checkout_rejected=true,updated_at=now() WHERE marca_id=$1 AND referencia_externa=$2 AND estado='CREATING' AND proveedor_suscripcion_id IS NULL`, brandID, reference)
	return err
}

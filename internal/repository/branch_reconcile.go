package repository

import (
	"clientesFrecuentes/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type BranchBillingProvider interface {
	CreateBranchCheckout(context.Context, model.BranchPaymentRequest) (model.BranchPaymentCheckout, error)
	FindBranchPayment(context.Context, string) (model.BillingPayment, bool, error)
	GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error)
	UpdateSubscriptionAmount(context.Context, string, int64, string) (model.BillingSubscriptionResult, error)
	RefundBranchPayment(context.Context, string, string) error
	ReconcileBranchDuplicatePayments(context.Context, string, string) error
}

func (r *Repository) PendingBranchOperations(ctx context.Context) ([]string, error) {
	rows, e := r.Pool.Query(ctx, `SELECT id::text FROM branch_operations WHERE (status IN('PAYMENT_PENDING','PLAN_UPDATING','REFUND_PENDING') OR status='FAILED' AND error_code='QUOTE_EXPIRED' AND NOT simulation OR status='COMPLETED' AND NOT simulation AND payment_id IS NOT NULL AND created_at>now()-interval '30 days') AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT 50`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return out, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (r *Repository) SimulateBranch(ctx context.Context, brand int64, id, action string, now time.Time) error {
	var payment, fault string
	switch action {
	case "approve":
		payment = "approved"
	case "reject":
		payment = "rejected"
	case "pending":
		payment = "pending"
	case "update_fail":
		payment = "approved"
		fault = "update_fail"
	case "permanent_fail":
		payment = "approved"
		fault = "permanent_fail"
	case "refund_fail":
		payment = "approved"
		fault = "refund_fail"
	case "recover":
		fault = ""
	default:
		return ErrInvalidRequest
	}
	tag, e := r.Pool.Exec(ctx, `UPDATE branch_operations SET payment_status=CASE WHEN $3<>'' THEN $3 ELSE payment_status END,payment_id=CASE WHEN $3='approved' THEN 'sim:'||id::text ELSE payment_id END,simulation_fault=$4,payment_approved_at=CASE WHEN $3='approved' THEN COALESCE(payment_approved_at,$5::timestamptz) ELSE payment_approved_at END,next_attempt_at=now(),updated_at=now() WHERE id=$1 AND marca_id=$2 AND simulation AND status IN('PAYMENT_PENDING','PLAN_UPDATING','REFUND_PENDING')`, id, brand, payment, fault, now)
	if e == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return e
}

// ReconcileBranch holds a brand/subscription/operation lock during bounded provider
// readbacks. External actions have stable operation idempotency keys. A crash after
// the external update/refund is recovered by reading provider state before retry.
func (r *Repository) ReconcileBranch(ctx context.Context, id string, provider BranchBillingProvider, appURL string, now time.Time) error {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var brand, actor int64
	e = tx.QueryRow(ctx, `SELECT marca_id,actor_id FROM branch_operations WHERE id=$1`, id).Scan(&brand, &actor)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	// Lock order is shared with branch creation and cancellation.
	_, e = tx.Exec(ctx, `SELECT id FROM marcas WHERE id=$1 FOR UPDATE`, brand)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `SELECT marca_id FROM suscripciones_marca WHERE marca_id=$1 FOR UPDATE`, brand)
	if e != nil {
		return e
	}
	var status, fault, paymentStatus, checkout, paymentID string
	var sim bool
	var attempts int
	var amount, refunded int64
	var planAmount *int64
	var qb, db, sb []byte
	var expiry time.Time
	var quoteCreated time.Time
	var approvedAt *time.Time
	var planStarted bool
	e = tx.QueryRow(ctx, `SELECT o.status,o.simulation,o.simulation_fault,o.payment_status,COALESCE(o.checkout_url,''),COALESCE(o.payment_id,''),o.attempts,o.payment_amount_minor,o.payment_refunded_minor,o.provider_plan_amount_minor,q.quote,q.draft,q.subscription_snapshot,q.expires_at,o.payment_approved_at,o.plan_update_started,q.created_at FROM branch_operations o JOIN branch_quotes q ON q.id=o.quote_id WHERE o.id=$1 FOR UPDATE OF o`, id).Scan(&status, &sim, &fault, &paymentStatus, &checkout, &paymentID, &attempts, &amount, &refunded, &planAmount, &qb, &db, &sb, &expiry, &approvedAt, &planStarted, &quoteCreated)
	if e != nil {
		return e
	}
	if status == "COMPLETED" && !sim && paymentID != "" {
		if provider == nil {
			return ErrInvalidRequest
		}
		duplicateErr := provider.ReconcileBranchDuplicatePayments(ctx, "puntazo:branch:"+id, paymentID)
		code := ""
		next := now.Add(time.Hour)
		if duplicateErr != nil {
			code = "DUPLICATE_REFUND_RETRY"
			next = now.Add(3 * time.Second)
		}
		_, e = tx.Exec(ctx, `UPDATE branch_operations SET error_code=$2,next_attempt_at=$3 WHERE id=$1`, id, code, next)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if status != "PAYMENT_PENDING" && status != "PLAN_UPDATING" && status != "REFUND_PENDING" && !(status == "FAILED" && !sim) {
		return nil
	}
	var quote model.BranchQuote
	var draft model.CreateBranchRequest
	var snapshot BranchSnapshot
	if e = json.Unmarshal(qb, &quote); e != nil {
		return e
	}
	if e = json.Unmarshal(db, &draft); e != nil {
		return e
	}
	if e = json.Unmarshal(sb, &snapshot); e != nil {
		return e
	}
	save := func(newStatus, code string) error {
		_, e = tx.Exec(ctx, `UPDATE branch_operations SET status=$2,error_code=$3,payment_approved_at=$11,plan_update_started=$12,payment_status=$4,payment_id=NULLIF($5,''),payment_amount_minor=$6,payment_refunded_minor=$7,provider_plan_amount_minor=$8,attempts=attempts+1,next_attempt_at=$9,updated_at=$10 WHERE id=$1`, id, newStatus, code, paymentStatus, paymentID, amount, refunded, planAmount, now.Add(3*time.Second), now, approvedAt, planStarted)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	valid, e := lockBranchBilling(ctx, tx, actor, brand)
	eligible := e == nil && branchSnapshotsEqual(valid, snapshot)
	if e != nil && !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrQuoteChanged) && !errors.Is(e, ErrSubscriptionChangeRequired) {
		return e
	}

	reference := "puntazo:branch:" + id
	if sim && provider != nil {
		return ErrInvalidRequest
	}
	if !sim && provider == nil {
		return ErrInvalidRequest
	}
	if status == "PAYMENT_PENDING" || status == "FAILED" {
		if !sim {
			payment, found, pe := provider.FindBranchPayment(ctx, reference)
			if pe != nil {
				return save(status, "PAYMENT_VERIFICATION_RETRY")
			}
			if found {
				paymentID = payment.ID
				paymentStatus = payment.Status
				amount = payment.AmountMinor
				refunded = payment.RefundedMinor
				approvedAt = payment.ApprovedAt
				if payment.ExternalReference != reference || payment.Currency != "ARS" || amount != quote.ProrationAmountCents {
					if paymentStatus == "approved" {
						status = "REFUND_PENDING"
					} else {
						return save("FAILED", "PAYMENT_MISMATCH")
					}
				}
			} else if checkout == "" && expiry.After(now) && eligible {
				var email string
				e = tx.QueryRow(ctx, `SELECT email::text FROM usuarios WHERE id=$1`, actor).Scan(&email)
				if e != nil {
					return e
				}
				result, pe := provider.CreateBranchCheckout(ctx, model.BranchPaymentRequest{Reference: reference, PayerEmail: email, BackURL: strings.TrimRight(appURL, "/") + "/sucursal-pago?brand_id=" + branchInt(brand) + "&operation_id=" + id, IdempotencyKey: id, AmountMinor: quote.ProrationAmountCents, ExpiresAt: expiry})
				if pe != nil {
					return save(status, "CHECKOUT_RETRY")
				}
				_, e = tx.Exec(ctx, `UPDATE branch_operations SET checkout_url=$2,checkout_id=$3 WHERE id=$1`, id, result.URL, result.ID)
				if e != nil {
					return e
				}
				return save(status, "")
			}
		}
		if paymentStatus == "rejected" || paymentStatus == "cancelled" {
			return save("PAYMENT_REJECTED", "PAYMENT_REJECTED")
		}
		if paymentStatus == "refunded" || refunded >= quote.ProrationAmountCents && quote.PaymentRequired {
			return save("REFUNDED", "")
		}
		if paymentStatus != "approved" {
			// A local simulator has no late external payment; real checkouts remain
			// reconciled after expiry, since a payment may settle asynchronously.
			if !expiry.After(now) {
				return save("FAILED", "QUOTE_EXPIRED")
			}
			return save(status, "")
		}
		if approvedAt == nil || approvedAt.Before(quoteCreated) || !expiry.After(*approvedAt) || refunded > 0 || !eligible || status == "REFUND_PENDING" {
			status = "REFUND_PENDING"
		} else {
			status = "PLAN_UPDATING"
		}
	}
	if status == "PLAN_UPDATING" {
		// Payment was verified earlier; all economic/security prerequisites are rechecked.
		if !eligible {
			status = "REFUND_PENDING"

		}
		if status == "PLAN_UPDATING" {
			if !sim && !planStarted {
				planStarted = true
				return save(status, "")
			}
			if sim {
				if fault == "update_fail" {
					return save(status, "PLAN_UPDATE_RETRY")
				}
				if fault == "permanent_fail" || fault == "refund_fail" {
					status = "REFUND_PENDING"
				} else {
					n := quote.NewMonthlyAmountCents
					planAmount = &n
				}
			} else {
				current, pe := provider.GetSubscription(ctx, snapshot.ProviderID)
				if pe != nil {
					return save(status, "PLAN_UPDATE_RETRY")
				}
				if current.ID != snapshot.ProviderID || current.ExternalReference != snapshot.Reference || current.Status != "authorized" || current.NextPaymentDate == nil || !current.NextPaymentDate.Equal(snapshot.Next) {
					status = "REFUND_PENDING"
				} else if planStarted && current.AmountMinor == quote.NewMonthlyAmountCents {
					n := current.AmountMinor
					planAmount = &n
				} else if current.AmountMinor != snapshot.Monthly {
					status = "REFUND_PENDING"
				} else {
					_, pe = provider.UpdateSubscriptionAmount(ctx, snapshot.ProviderID, quote.NewMonthlyAmountCents, id+":plan")
					if pe != nil {
						var rejection interface{ Rejected() bool }
						if errors.As(pe, &rejection) && rejection.Rejected() {
							status = "REFUND_PENDING"
						} else {
							return save(status, "PLAN_UPDATE_RETRY")
						}
					}
					if status == "PLAN_UPDATING" {
						verified, ve := provider.GetSubscription(ctx, snapshot.ProviderID)
						if ve != nil {
							return save(status, "PLAN_UPDATE_RETRY")
						}
						if verified.AmountMinor != quote.NewMonthlyAmountCents || verified.NextPaymentDate == nil || !verified.NextPaymentDate.Equal(snapshot.Next) || verified.Status != "authorized" {
							return save(status, "PLAN_UPDATE_RETRY")
						}
						n := verified.AmountMinor
						planAmount = &n
					}
				}
			}
			if status == "PLAN_UPDATING" && planAmount != nil && *planAmount == quote.NewMonthlyAmountCents {
				// Preserve old recurring invoices across a quantity-only transition.
				_, e = tx.Exec(ctx, `INSERT INTO subscription_quantity_history(marca_id,operation_id,unit_price_minor,full_unit_price_minor,branches,valid_from,valid_until) VALUES($1,$2,$3,$4,$5,GREATEST(COALESCE((SELECT max(valid_until) FROM subscription_quantity_history WHERE marca_id=$1),'-infinity'::timestamptz),(SELECT COALESCE(price_valid_from,created_at) FROM suscripciones_marca WHERE marca_id=$1)),$6) ON CONFLICT(operation_id) DO NOTHING`, brand, id, snapshot.Unit, snapshot.FullUnit, snapshot.Count, now)
				if e != nil {
					return e
				}
				var branch model.Branch
				e = scanBranch(tx.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,direccion,localidad,provincia,codigo_postal,latitud,longitud) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,marca_id,nombre,direccion,activo,localidad,provincia,codigo_postal,latitud,longitud,principal,version,created_at,updated_at`, brand, draft.Name, draft.Address, draft.Locality, draft.Province, draft.PostalCode, draft.Latitude, draft.Longitude), &branch)
				if e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `UPDATE suscripciones_marca SET cantidad_sucursales=cantidad_sucursales+1,importe_mensual_minor=$2,updated_at=$3 WHERE marca_id=$1`, brand, quote.NewMonthlyAmountCents, now)
				if e != nil {
					return e
				}
				_, e = tx.Exec(ctx, `UPDATE branch_operations SET branch_id=$2 WHERE id=$1`, id, branch.ID)
				if e != nil {
					return e
				}
				return save("COMPLETED", "")
			}
		}
	}
	if status == "REFUND_PENDING" {
		if sim {
			if !quote.PaymentRequired {
				return save("FAILED", "PLAN_UPDATE_FAILED")
			}
			if fault == "refund_fail" {
				return save(status, "REFUND_RETRY")
			}
			refunded = amount
			paymentStatus = "refunded"
			return save("REFUNDED", "")
		}
		// Restore the previous recurring amount before refunding, including a crash
		// after provider PUT but before the database activation transaction committed.
		current, pe := provider.GetSubscription(ctx, snapshot.ProviderID)
		if pe != nil {
			return save(status, "PLAN_RESTORE_RETRY")
		}
		if current.ID != snapshot.ProviderID || current.ExternalReference != snapshot.Reference || (planStarted && (current.Status != "authorized" || current.NextPaymentDate == nil || !current.NextPaymentDate.Equal(snapshot.Next))) {
			return save(status, "PLAN_RESTORE_REVIEW_REQUIRED")
		}
		var sameProvider bool
		if e = tx.QueryRow(ctx, `SELECT proveedor_suscripcion_id=$2 AND referencia_externa=$3 AND precio_sucursal_minor=$4 AND importe_mensual_minor=$5 AND cantidad_sucursales=$6 AND proximo_cobro_at=$7 AND estado='AUTHORIZED' FROM suscripciones_marca WHERE marca_id=$1`, brand, snapshot.ProviderID, snapshot.Reference, snapshot.Unit, snapshot.Monthly, snapshot.Count, snapshot.Next).Scan(&sameProvider); e != nil {
			return e
		}
		if !sameProvider && planStarted {
			return save(status, "PLAN_RESTORE_REVIEW_REQUIRED")
		}
		if planStarted && current.AmountMinor == quote.NewMonthlyAmountCents {
			_, pe = provider.UpdateSubscriptionAmount(ctx, snapshot.ProviderID, snapshot.Monthly, id+":restore")
			if pe != nil {
				return save(status, "PLAN_RESTORE_RETRY")
			}
			check, ce := provider.GetSubscription(ctx, snapshot.ProviderID)
			if ce != nil || check.ID != snapshot.ProviderID || check.ExternalReference != snapshot.Reference || check.AmountMinor != snapshot.Monthly || check.Status != "authorized" || check.NextPaymentDate == nil || !check.NextPaymentDate.Equal(snapshot.Next) {
				return save(status, "PLAN_RESTORE_RETRY")
			}
		}
		if planStarted && current.AmountMinor != snapshot.Monthly && current.AmountMinor != quote.NewMonthlyAmountCents {
			return save(status, "PLAN_RESTORE_REVIEW_REQUIRED")
		}
		if !quote.PaymentRequired {
			return save("FAILED", "PLAN_UPDATE_FAILED")
		}
		if paymentID == "" {
			return save(status, "PAYMENT_VERIFICATION_RETRY")
		}
		if refunded >= amount && amount > 0 {
			return save("REFUNDED", "")
		}
		if pe = provider.RefundBranchPayment(ctx, paymentID, id+":refund"); pe != nil {
			return save(status, "REFUND_RETRY")
		}
		payment, found, pe := provider.FindBranchPayment(ctx, reference)
		if pe != nil || !found || payment.RefundedMinor < amount {
			return save(status, "REFUND_RETRY")
		}
		refunded = payment.RefundedMinor
		paymentStatus = payment.Status
		return save("REFUNDED", "")
	}
	return fmt.Errorf("unexpected branch billing state %s", status)
}

func requireNoPendingBranch(ctx context.Context, tx pgx.Tx, brand int64) error {
	var pending bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM branch_operations WHERE marca_id=$1 AND status IN('PAYMENT_PENDING','PLAN_UPDATING','REFUND_PENDING'))`, brand).Scan(&pending)
	if e != nil {
		return e
	}
	if pending {
		return ErrIdempotencyInProgress
	}
	return nil
}

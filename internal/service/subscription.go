package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"github.com/google/uuid"
)

func (s *Service) Subscription(ctx context.Context, actorID, brandID int64) (model.Subscription, error) {
	billing, err := s.Repo.BillingContext(ctx, actorID, brandID)
	if err != nil {
		return model.Subscription{}, err
	}
	unitPrice, ok := s.subscriptionUnitPrice(billing.ProgramType)
	if !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	record, err := s.Repo.GetSubscriptionRecord(ctx, brandID)
	if errors.Is(err, repository.ErrNotFound) {
		price, priceErr := s.Repo.SubscriptionPrice(ctx, billing.ProgramType, unitPrice)
		if priceErr != nil {
			return model.Subscription{}, priceErr
		}
		unitPrice = price.UnitPriceMinor
		discounted, remaining, priceErr := s.Repo.ReferralCheckoutPrice(ctx, brandID, unitPrice)
		if priceErr != nil {
			return model.Subscription{}, priceErr
		}
		out := model.Subscription{BrandID: brandID, Provider: "MERCADO_PAGO", Status: "NOT_CONFIGURED", Currency: "ARS", UnitAmountCents: discounted, ActiveBranches: billing.ActiveBranches, MonthlyAmountCents: discounted * billing.ActiveBranches, FullMonthlyAmountCents: unitPrice * billing.ActiveBranches, DiscountRemainingCharges: remaining, ProviderConfigured: (s.Billing != nil || s.Config.BranchPaymentSimulator), UpdatedAt: s.Now()}
		s.setSubscriptionTrial(&out, billing)
		return out, nil
	}
	if err != nil {
		return model.Subscription{}, err
	}
	needsProviderRefresh := record.ProviderID != ""
	if record.Subscription.Status == "CREATING" && s.Billing != nil {
		if finder, ok := s.Billing.(subscriptionFinder); ok {
			provider, found, lookupErr := finder.FindSubscription(ctx, record.ExternalReference)
			if lookupErr != nil {
				return model.Subscription{}, ErrBillingProviderFailure
			}
			if found {
				if provider.ExternalReference != record.ExternalReference || provider.ID == "" {
					return model.Subscription{}, ErrBillingProviderFailure
				}
				if _, saveErr := s.Repo.SaveSubscriptionCheckout(ctx, brandID, provider); saveErr != nil && !errors.Is(saveErr, repository.ErrConflict) {
					return model.Subscription{}, saveErr
				}
				record, err = s.Repo.GetSubscriptionRecord(ctx, brandID)
				if err != nil {
					return model.Subscription{}, err
				}
			}
		}
	}
	if needsProviderRefresh && record.Subscription.Status != "CANCELLED" && s.Billing != nil {
		provider, lookupErr := s.Billing.GetSubscription(ctx, record.ProviderID)
		if lookupErr != nil || provider.ID != record.ProviderID || provider.ExternalReference != record.ExternalReference {
			return model.Subscription{}, ErrBillingProviderFailure
		}
		if _, updateErr := s.Repo.UpdateSubscriptionFromProvider(ctx, provider); updateErr != nil {
			return model.Subscription{}, updateErr
		}
		record, err = s.Repo.GetSubscriptionRecord(ctx, brandID)
		if err != nil {
			return model.Subscription{}, err
		}
	}
	s.setSubscriptionTrial(&record.Subscription, billing)
	record.Subscription.ProviderConfigured = s.Billing != nil || s.Config.BranchPaymentSimulator
	return record.Subscription, nil
}

func (s *Service) CreateSubscriptionCheckout(ctx context.Context, actorID, brandID int64, idempotencyKey string) (model.Subscription, error) {
	if s.Billing == nil {
		return model.Subscription{}, ErrBillingUnavailable
	}
	key, err := uuid.Parse(strings.TrimSpace(idempotencyKey))
	if err != nil {
		return model.Subscription{}, ErrInvalidRequest
	}
	billingContext, contextErr := s.Repo.BillingContext(ctx, actorID, brandID)
	if contextErr != nil {
		return model.Subscription{}, contextErr
	}
	if billingContext.TrialStartEstimated || billingContext.TrialStartedAt == nil {
		return model.Subscription{}, ErrTrialStartUnknown
	}
	external := fmt.Sprintf("puntazo:brand:%d:%s", brandID, key.String())
	billing, reserved, err := s.Repo.ReserveSubscriptionCheckout(ctx, actorID, brandID, external, s.Config.MercadoPagoBranchPrice, s.Config.MercadoPagoPointsPrice)
	if errors.Is(err, repository.ErrConflict) {
		return model.Subscription{}, ErrSubscriptionExists
	}
	if err != nil {
		return model.Subscription{}, err
	}
	if reserved.Subscription.Status != "CREATING" {
		s.setSubscriptionTrial(&reserved.Subscription, billing)
		reserved.Subscription.ProviderConfigured = true
		return reserved.Subscription, nil
	}
	claimed, err := s.Repo.ClaimSubscriptionProviderCall(ctx, brandID, reserved.ExternalReference)
	if err != nil {
		return model.Subscription{}, err
	}
	if !claimed {
		existing, lookupErr := s.Repo.GetSubscriptionRecord(ctx, brandID)
		if lookupErr == nil && existing.ExternalReference == reserved.ExternalReference && existing.Subscription.Status != "CREATING" {
			existing.Subscription.ProviderConfigured = true
			return existing.Subscription, nil
		}
		return model.Subscription{}, ErrBillingInProgress
	}
	providerKey := strings.TrimPrefix(reserved.ExternalReference, fmt.Sprintf("puntazo:brand:%d:", brandID))
	if _, err = uuid.Parse(providerKey); err != nil {
		return model.Subscription{}, ErrInvalidRequest
	}
	var startDate *time.Time
	freeTrialMonths := reserved.TrialMonths
	if reserved.Subscription.TrialEndsAt != nil {
		freeTrialMonths = 0
		if reserved.Subscription.TrialEndsAt.After(s.Now()) {
			startDate = reserved.Subscription.TrialEndsAt
		}
	}
	created, err := s.Billing.CreateSubscription(ctx, model.BillingSubscriptionRequest{
		Reason: fmt.Sprintf("Puntazo %s mensual · %d sucursal(es)", billing.ProgramType, reserved.Subscription.ActiveBranches), ExternalReference: reserved.ExternalReference,
		PayerEmail: billing.PayerEmail, BackURL: strings.TrimRight(s.Config.PublicAppURL, "/") + "/suscripcion/resultado",
		IdempotencyKey: providerKey, Currency: "ARS", AmountMinor: reserved.Subscription.MonthlyAmountCents, FreeTrialMonths: freeTrialMonths, StartDate: startDate,
	})
	if err != nil {
		var rejection interface{ Rejected() bool }
		var httpFailure interface{ HTTPStatus() int }
		status := 0
		if errors.As(err, &httpFailure) {
			status = httpFailure.HTTPStatus()
		}
		slog.WarnContext(ctx, "subscription_provider_create_failed", "brand_id", brandID, "provider_http_status", status, "error_type", fmt.Sprintf("%T", err))
		if errors.As(err, &rejection) && rejection.Rejected() {
			if releaseErr := s.Repo.RejectSubscriptionCheckout(ctx, brandID, reserved.ExternalReference); releaseErr != nil {
				return model.Subscription{}, releaseErr
			}
			return model.Subscription{}, ErrBillingRejected
		}
		return model.Subscription{}, ErrBillingProviderFailure
	}
	if created.ExternalReference != reserved.ExternalReference || created.ID == "" {
		return model.Subscription{}, ErrBillingProviderFailure
	}
	out, err := s.Repo.SaveSubscriptionCheckout(ctx, brandID, created)
	if errors.Is(err, repository.ErrConflict) {
		existing, lookupErr := s.Repo.GetSubscriptionRecord(ctx, brandID)
		if lookupErr == nil && existing.ExternalReference == reserved.ExternalReference && existing.Subscription.Status != "CREATING" {
			existing.Subscription.ProviderConfigured = true
			return existing.Subscription, nil
		}
	}
	if err != nil {
		return out, err
	}
	current, err := s.Repo.GetSubscriptionRecord(ctx, brandID)
	if err != nil {
		return out, err
	}
	s.setSubscriptionTrial(&current.Subscription, billing)
	current.Subscription.ProviderConfigured = true
	return current.Subscription, nil
}

func (s *Service) ApplySubscriptionWebhook(ctx context.Context, notificationID, topic, resourceID string) error {
	if s.Billing == nil {
		return ErrBillingUnavailable
	}
	if notificationID == "" || resourceID == "" {
		return ErrInvalidRequest
	}
	if topic == "payment" && s.Config.BranchProrationEnabled {
		handled, e := s.applyBranchPaymentWebhook(ctx, resourceID)
		if handled || e != nil {
			return e
		}
	}
	if topic == "subscription_authorized_payment" || topic == "payment" {
		return s.applyReferralPaymentWebhook(ctx, notificationID, topic, resourceID)
	}
	if topic != "subscription_preapproval" {
		return ErrInvalidRequest
	}
	provider, err := s.Billing.GetSubscription(ctx, resourceID)
	if err != nil {
		return ErrBillingProviderFailure
	}
	if provider.ID != resourceID || !strings.HasPrefix(provider.ExternalReference, "puntazo:brand:") {
		return ErrInvalidRequest
	}
	if s.Config.MailProvider == "smtp" || s.Config.MailProvider == "capture" {
		return s.Repo.RecordSubscriptionWebhookWithConfirmation(ctx, notificationID, topic, provider)
	}
	return s.Repo.RecordSubscriptionWebhook(ctx, notificationID, topic, provider)
}

type referralBillingProvider interface {
	GetAuthorizedPayment(context.Context, string) (model.BillingInvoice, error)
	GetPayment(context.Context, string) (model.BillingPayment, error)
	UpdateSubscriptionAmount(context.Context, string, int64, string) (model.BillingSubscriptionResult, error)
}

var settlementReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_:-]{5,119}$`)

// RecordManualMerchantCredit records a Finance attestation of a completed
// external reimbursement. The provider invoice and payment are read back here;
// the external settlement itself remains an operator-supplied reference.
func (s *Service) RecordManualMerchantCredit(ctx context.Context, brandID, financeUserID, amountMinor int64, idempotencyKey, invoiceID, externalReference string) (repository.MerchantCreditAllocation, error) {
	var zero repository.MerchantCreditAllocation
	provider, ok := s.Billing.(referralBillingProvider)
	if !ok {
		return zero, ErrBillingUnavailable
	}
	key, err := uuid.Parse(strings.TrimSpace(idempotencyKey))
	if err != nil || brandID < 1 || financeUserID < 1 || amountMinor < 1 || !settlementReferencePattern.MatchString(externalReference) {
		return zero, ErrInvalidRequest
	}
	invoice, err := provider.GetAuthorizedPayment(ctx, invoiceID)
	if err != nil {
		return zero, ErrBillingProviderFailure
	}
	if invoice.ID != invoiceID || invoice.SubscriptionID == "" || invoice.Currency != "ARS" || invoice.PaymentID == "" || invoice.AmountMinor < 1 {
		return zero, ErrInvalidRequest
	}
	payment, err := provider.GetPayment(ctx, invoice.PaymentID)
	if err != nil {
		return zero, ErrBillingProviderFailure
	}
	if payment.ID != invoice.PaymentID || payment.Status != "approved" || payment.Currency != "ARS" || payment.AmountMinor != invoice.AmountMinor || payment.RefundedMinor != 0 {
		return zero, ErrInvalidRequest
	}
	return s.Repo.RecordMerchantCreditAllocation(ctx, brandID, financeUserID, amountMinor, key, externalReference, invoice, payment)
}

func (s *Service) applyReferralPaymentWebhook(ctx context.Context, notificationID, topic, resourceID string) error {
	provider, ok := s.Billing.(referralBillingProvider)
	if !ok {
		return ErrBillingUnavailable
	}
	if topic == "payment" {
		if _, err := s.Repo.ReferralInvoiceForPayment(ctx, resourceID); errors.Is(err, repository.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		payment, err := provider.GetPayment(ctx, resourceID)
		if err != nil {
			return ErrBillingProviderFailure
		}
		if payment.ID != resourceID || payment.Currency != "ARS" || payment.AmountMinor < 1 || payment.RefundedMinor < 0 || payment.RefundedMinor > payment.AmountMinor {
			return ErrInvalidRequest
		}
		return s.Repo.ReverseReferralPayment(ctx, notificationID, payment)
	}
	invoiceID := resourceID
	invoice, err := provider.GetAuthorizedPayment(ctx, invoiceID)
	if err != nil {
		return ErrBillingProviderFailure
	}
	if invoice.ID != invoiceID || invoice.SubscriptionID == "" || invoice.Currency != "ARS" || invoice.AmountMinor < 0 {
		return ErrInvalidRequest
	}
	if invoice.PaymentID == "" || invoice.AmountMinor == 0 {
		return nil
	} // scheduled, failed or free invoices do not earn rewards
	payment, err := provider.GetPayment(ctx, invoice.PaymentID)
	if err != nil {
		return ErrBillingProviderFailure
	}
	if payment.ID != invoice.PaymentID || payment.Currency != "ARS" || payment.AmountMinor != invoice.AmountMinor || payment.RefundedMinor < 0 || payment.RefundedMinor > payment.AmountMinor {
		return ErrInvalidRequest
	}
	advance := func(ctx context.Context, id string, amountMinor int64, key string) error {
		updated, err := provider.UpdateSubscriptionAmount(ctx, id, amountMinor, key)
		if err != nil {
			return ErrBillingProviderFailure
		}
		if updated.ID != id || updated.AmountMinor != amountMinor {
			return ErrBillingProviderFailure
		}
		return nil
	}
	return s.Repo.RecordReferralInvoice(ctx, notificationID, invoice, payment, advance)
}

func (s *Service) CancelSubscription(ctx context.Context, actorID, brandID int64, idempotencyKey string) (model.Subscription, error) {
	if s.Billing == nil {
		return model.Subscription{}, ErrBillingUnavailable
	}
	key, err := uuid.Parse(strings.TrimSpace(idempotencyKey))
	if err != nil {
		return model.Subscription{}, ErrInvalidRequest
	}
	billing, err := s.Repo.BillingContext(ctx, actorID, brandID)
	if err != nil {
		return model.Subscription{}, err
	}
	if _, ok := s.subscriptionUnitPrice(billing.ProgramType); !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	if s.Config.BranchProrationEnabled {
		out, e := s.Repo.CancelSubscriptionLocked(ctx, actorID, brandID, func(record repository.SubscriptionRecord) (model.BillingSubscriptionResult, error) {
			if record.Subscription.Status == "CANCELLED" {
				return model.BillingSubscriptionResult{ID: record.ProviderID, ExternalReference: record.ExternalReference, Status: "cancelled"}, nil
			}
			if record.Subscription.Status == "CREATING" {
				return model.BillingSubscriptionResult{}, ErrBillingInProgress
			}
			provider, e := s.Billing.CancelSubscription(ctx, record.ProviderID, key.String())
			if e != nil {
				return provider, ErrBillingProviderFailure
			}
			if provider.ID != record.ProviderID || provider.ExternalReference != record.ExternalReference || (provider.Status != "cancelled" && provider.Status != "canceled") {
				return provider, ErrBillingProviderFailure
			}
			return provider, nil
		})
		out.ProviderConfigured = true
		return out, e
	}
	record, err := s.Repo.GetSubscriptionRecord(ctx, brandID)
	if err != nil {
		return model.Subscription{}, err
	}
	if record.Subscription.Status == "CANCELLED" {
		if record.Subscription.NextPaymentDate != nil || record.Subscription.CheckoutURL != "" {
			// Older cancellations may retain metadata that Mercado Pago returns
			// even after renewals have stopped. Reconcile it without another PUT.
			_, err = s.Repo.UpdateSubscriptionFromProvider(ctx, model.BillingSubscriptionResult{
				ID: record.ProviderID, ExternalReference: record.ExternalReference, Status: "cancelled",
			})
			if err != nil {
				return model.Subscription{}, err
			}
			record, err = s.Repo.GetSubscriptionRecord(ctx, brandID)
			if err != nil {
				return model.Subscription{}, err
			}
		}
		record.Subscription.ProviderConfigured = true
		return record.Subscription, nil
	}
	if record.Subscription.Status == "CREATING" {
		return model.Subscription{}, ErrBillingInProgress
	}
	provider, err := s.Billing.CancelSubscription(ctx, record.ProviderID, key.String())
	if err != nil {
		return model.Subscription{}, ErrBillingProviderFailure
	}
	if provider.ID != record.ProviderID || provider.ExternalReference != record.ExternalReference {
		return model.Subscription{}, ErrBillingProviderFailure
	}
	out, err := s.Repo.UpdateSubscriptionFromProvider(ctx, provider)
	if err != nil {
		return out, err
	}
	current, err := s.Repo.GetSubscriptionRecord(ctx, brandID)
	if err != nil {
		return out, err
	}
	current.Subscription.ProviderConfigured = true
	return current.Subscription, nil
}

func (s *Service) subscriptionUnitPrice(programType string) (int64, bool) {
	switch programType {
	case "SELLOS":
		return s.Config.MercadoPagoBranchPrice, true
	case "PUNTOS":
		return s.Config.MercadoPagoPointsPrice, true
	default:
		return 0, false
	}
}

var subscriptionProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (s *Service) SubscriptionResult(ctx context.Context, actorID int64, providerID string) (model.Subscription, error) {
	if !subscriptionProviderIDPattern.MatchString(providerID) {
		return model.Subscription{}, ErrInvalidRequest
	}
	brandID, err := s.Repo.SubscriptionBrandForOwner(ctx, actorID, providerID)
	if err != nil {
		return model.Subscription{}, err
	}
	return s.Subscription(ctx, actorID, brandID)
}

func (s *Service) RequestSubscriptionConfirmation(ctx context.Context, actorID, brandID int64) error {
	if s.Config.MailProvider != "smtp" && s.Config.MailProvider != "capture" {
		return repository.ErrEmailUnavailable
	}
	return s.Repo.RequestSubscriptionConfirmation(ctx, actorID, brandID)
}

type subscriptionFinder interface {
	FindSubscription(context.Context, string) (model.BillingSubscriptionResult, bool, error)
}

func (s *Service) setSubscriptionTrial(out *model.Subscription, billing repository.BillingContext) {
	out.TrialStartedAt = billing.TrialStartedAt
	out.TrialStartEstimated = billing.TrialStartEstimated
	// Display the account's known deadline without rewriting a legacy reservation
	// or the provider's next-payment contract.
	if out.TrialEndsAt == nil && billing.TrialStartedAt != nil && !billing.TrialStartEstimated {
		end := repository.TrialEnd(*billing.TrialStartedAt)
		out.TrialEndsAt = &end
	}
	if out.TrialEndsAt != nil {
		out.TrialAvailable = out.TrialEndsAt.After(s.Now())
	}
}

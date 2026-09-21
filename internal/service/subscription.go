package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
		return model.Subscription{BrandID: brandID, Provider: "MERCADO_PAGO", Status: "NOT_CONFIGURED", Currency: "ARS", UnitAmountCents: unitPrice, ActiveBranches: billing.ActiveBranches, MonthlyAmountCents: unitPrice * billing.ActiveBranches, ProviderConfigured: s.Billing != nil, TrialAvailable: true, UpdatedAt: s.Now()}, nil
	}
	if err != nil {
		return model.Subscription{}, err
	}
	record.Subscription.ProviderConfigured = s.Billing != nil
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
	billing, err := s.Repo.BillingContext(ctx, actorID, brandID)
	if err != nil {
		return model.Subscription{}, err
	}
	unitPrice, ok := s.subscriptionUnitPrice(billing.ProgramType)
	if !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	external := fmt.Sprintf("puntazo:brand:%d:%s", brandID, key.String())
	trialMonths := 1
	if existing, existingErr := s.Repo.GetSubscriptionRecord(ctx, brandID); existingErr == nil {
		trialMonths = 0
		if existing.ExternalReference == external {
			existing.Subscription.ProviderConfigured = true
			return existing.Subscription, nil
		}
		if existing.Subscription.Status != "CANCELLED" {
			return model.Subscription{}, ErrSubscriptionExists
		}
	} else if !errors.Is(existingErr, repository.ErrNotFound) {
		return model.Subscription{}, existingErr
	}
	amount := unitPrice * billing.ActiveBranches
	created, err := s.Billing.CreateSubscription(ctx, model.BillingSubscriptionRequest{
		Reason: fmt.Sprintf("Puntazo %s mensual · %d sucursal(es)", billing.ProgramType, billing.ActiveBranches), ExternalReference: external,
		PayerEmail: billing.PayerEmail, BackURL: strings.TrimRight(s.Config.PublicAppURL, "/") + "/suscripcion/resultado",
		IdempotencyKey: key.String(), Currency: "ARS", Amount: float64(amount) / 100, FreeTrialMonths: trialMonths,
	})
	if err != nil {
		return model.Subscription{}, ErrBillingUnavailable
	}
	if created.ExternalReference != external {
		return model.Subscription{}, ErrBillingUnavailable
	}
	out, err := s.Repo.SaveSubscriptionCheckout(ctx, brandID, unitPrice, billing.ActiveBranches, created)
	out.ProviderConfigured = true
	return out, err
}

func (s *Service) ApplySubscriptionWebhook(ctx context.Context, notificationID, topic, resourceID string) error {
	if s.Billing == nil {
		return ErrBillingUnavailable
	}
	if notificationID == "" || topic != "subscription_preapproval" || resourceID == "" {
		return ErrInvalidRequest
	}
	provider, err := s.Billing.GetSubscription(ctx, resourceID)
	if err != nil {
		return ErrBillingUnavailable
	}
	if provider.ID != resourceID || !strings.HasPrefix(provider.ExternalReference, "puntazo:brand:") {
		return ErrInvalidRequest
	}
	return s.Repo.RecordSubscriptionWebhook(ctx, notificationID, topic, provider)
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
	record, err := s.Repo.GetSubscriptionRecord(ctx, brandID)
	if err != nil {
		return model.Subscription{}, err
	}
	if record.Subscription.Status == "CANCELLED" {
		record.Subscription.ProviderConfigured = true
		return record.Subscription, nil
	}
	provider, err := s.Billing.CancelSubscription(ctx, record.ProviderID, key.String())
	if err != nil {
		return model.Subscription{}, ErrBillingUnavailable
	}
	if provider.ID != record.ProviderID || provider.ExternalReference != record.ExternalReference {
		return model.Subscription{}, ErrBillingUnavailable
	}
	out, err := s.Repo.UpdateSubscriptionFromProvider(ctx, provider)
	out.ProviderConfigured = true
	return out, err
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

package service

import (
	"clientesFrecuentes/internal/applebilling"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"context"
	"errors"
	"log/slog"
	"time"
)

var ErrAppleTransactionInvalid = errors.New("invalid Apple transaction")

type AppleBillingProvider interface {
	Enabled() bool
	PurchasesEnabled() bool
	Environment() string
	VerifyTransaction(context.Context, string) (model.AppleTransaction, error)
	VerifyNotification(context.Context, string) (model.AppleNotification, error)
	SubscriptionStatuses(context.Context, string) ([]model.AppleSubscriptionStatus, error)
	SetAppAccountToken(context.Context, string, string) error
}

func (s *Service) BrandBilling(ctx context.Context, actor, brand int64) (model.BrandBillingState, error) {
	return s.Repo.BrandBillingState(ctx, actor, brand)
}
func (s *Service) AppleBillingCatalog(ctx context.Context, actor, brand int64) (model.AppleCatalog, error) {
	state, e := s.BrandBilling(ctx, actor, brand)
	if e != nil {
		return model.AppleCatalog{}, e
	}
	enabled := s.AppleBilling != nil && s.AppleBilling.PurchasesEnabled()
	// Ownership is rechecked under the brand lock when an intent is created.
	_, ownerErr := s.Repo.BillingContext(ctx, actor, brand)
	eligible := enabled && ownerErr == nil && state.Mode != "MERCADO_PAGO" && state.TrialEndsAt != nil && !s.Now().Before(*state.TrialEndsAt)
	return model.AppleCatalog{Enabled: enabled, Products: applebilling.Catalog(), TrialEndsAt: state.TrialEndsAt, EligibleToPurchase: eligible}, nil
}
func (s *Service) CreateAppleBillingIntent(ctx context.Context, actor, brand int64, key string, req model.ApplePurchaseIntentRequest) (model.ApplePurchaseIntent, error) {
	if s.AppleBilling == nil || !s.AppleBilling.PurchasesEnabled() {
		return model.ApplePurchaseIntent{}, ErrBillingUnavailable
	}
	return s.Repo.CreateApplePurchaseIntent(ctx, actor, brand, key, req, s.AppleBilling.Environment())
}
func (s *Service) SyncApplePurchase(ctx context.Context, actor, brand int64, req model.AppleSyncRequest) (model.BrandBillingState, error) {
	if s.AppleBilling == nil || !s.AppleBilling.Enabled() {
		return model.BrandBillingState{}, ErrBillingUnavailable
	}
	t, e := s.AppleBilling.VerifyTransaction(ctx, req.SignedTransaction)
	if e != nil {
		if errors.Is(e, applebilling.ErrUnavailable) {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
		return model.BrandBillingState{}, ErrAppleTransactionInvalid
	}
	// Explicit restore of a tokenless receipt starts with Apple's current state,
	// not a potentially old signed transaction supplied by the device.
	var statuses []model.AppleSubscriptionStatus
	if req.Restore && t.AppAccountToken == "" {
		if _, e = s.Repo.BillingContext(ctx, actor, brand); e != nil {
			return model.BrandBillingState{}, e
		}
		statuses, e = s.AppleBilling.SubscriptionStatuses(ctx, t.OriginalTransactionID)
		if e != nil || len(statuses) == 0 {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
		original := t.OriginalTransactionID
		for i, v := range statuses {
			if v.Transaction.OriginalTransactionID != original {
				return model.BrandBillingState{}, ErrBillingProviderFailure
			}
			if i == 0 || v.Transaction.SignedDate > t.SignedDate {
				t = v.Transaction
			}
		}
		for _, v := range statuses {
			if v.Transaction.AppAccountToken != "" && v.Transaction.AppAccountToken != t.AppAccountToken {
				return model.BrandBillingState{}, repository.ErrAppleChainConflict
			}
		}
	}
	var token string
	if req.Restore {
		token, e = s.Repo.AppleRestoreClaim(ctx, actor, brand, t)
	} else {
		token, e = s.Repo.AppleClaim(ctx, actor, brand, t, req.IntentID)
	}
	if e != nil {
		return model.BrandBillingState{}, e
	}
	for _, v := range statuses {
		if v.Transaction.AppAccountToken != "" && v.Transaction.AppAccountToken != token {
			return model.BrandBillingState{}, repository.ErrAppleChainConflict
		}
	}
	if t.AppAccountToken == "" {
		// A code redemption is claimed durably before assignment; never replace an existing token.
		statuses, e = s.AppleBilling.SubscriptionStatuses(ctx, t.OriginalTransactionID)
		if e != nil {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
		for _, v := range statuses {
			if v.Transaction.AppAccountToken != "" && v.Transaction.AppAccountToken != token {
				return model.BrandBillingState{}, repository.ErrAppleChainConflict
			}
		}
		if e = s.AppleBilling.SetAppAccountToken(ctx, t.OriginalTransactionID, token); e != nil {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
		statuses, e = s.AppleBilling.SubscriptionStatuses(ctx, t.OriginalTransactionID)
		if e != nil {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
		matched := false
		for _, v := range statuses {
			if v.Transaction.OriginalTransactionID != t.OriginalTransactionID {
				return model.BrandBillingState{}, ErrBillingProviderFailure
			}
			if v.Transaction.AppAccountToken != "" && v.Transaction.AppAccountToken != token {
				return model.BrandBillingState{}, repository.ErrAppleChainConflict
			}
			if v.Transaction.AppAccountToken == token {
				t = v.Transaction
				matched = true
			}
		}
		if !matched {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
	}
	if t.AppAccountToken != token {
		return model.BrandBillingState{}, repository.ErrAppleChainConflict
	}
	// Xcode StoreKit testing is offline; production/sandbox always reconcile authoritative statuses.
	if s.AppleBilling.Environment() != "Xcode" {
		statuses, e = s.AppleBilling.SubscriptionStatuses(ctx, t.OriginalTransactionID)
		if e != nil {
			return model.BrandBillingState{}, ErrBillingProviderFailure
		}
		for _, v := range statuses {
			if e = s.Repo.SaveAppleTransaction(ctx, actor, brand, v.Transaction, &v.Renewal); e != nil {
				return model.BrandBillingState{}, e
			}
		}
	}
	if s.AppleBilling.Environment() == "Xcode" {
		if e = s.Repo.SaveAppleTransaction(ctx, actor, brand, t, nil); e != nil {
			return model.BrandBillingState{}, e
		}
	}
	return s.BrandBilling(ctx, actor, brand)
}
func (s *Service) ReceiveAppleNotification(ctx context.Context, signed string) error {
	if s.AppleBilling == nil || !s.AppleBilling.Enabled() {
		return ErrBillingUnavailable
	}
	n, e := s.AppleBilling.VerifyNotification(ctx, signed)
	if e != nil {
		if errors.Is(e, applebilling.ErrUnavailable) {
			return ErrBillingProviderFailure
		}
		return ErrAppleTransactionInvalid
	}
	return s.Repo.StoreAppleNotification(ctx, n, signed)
}
func (s *Service) ProcessAppleNotifications(ctx context.Context) error {
	if s.AppleBilling == nil || !s.AppleBilling.Enabled() {
		return nil
	}
	items, e := s.Repo.LeaseAppleNotifications(ctx, s.AppleBilling.Environment())
	if e != nil {
		return e
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		success := false
		n, err := s.AppleBilling.VerifyNotification(ctx, item.Payload)
		if err == nil {
			if n.Transaction == nil {
				success = true
			} else {
				t := *n.Transaction
				brand, err := s.Repo.AppleBrandForTransaction(ctx, t)
				if err == nil {
					// Persist signed revocations immediately. Tokenless code events are
					// completed only after the now-bound canonical transaction is fetched.
					if t.AppAccountToken != "" {
						err = s.Repo.SaveAppleTransaction(ctx, 0, brand, t, n.Renewal)
					}
					if err == nil && s.AppleBilling.Environment() != "Xcode" {
						var statuses []model.AppleSubscriptionStatus
						statuses, err = s.AppleBilling.SubscriptionStatuses(ctx, t.OriginalTransactionID)
						if err == nil {
							for _, v := range statuses {
								if err = s.Repo.SaveAppleTransaction(ctx, 0, brand, v.Transaction, &v.Renewal); err != nil {
									break
								}
							}
						}
					}
					success = err == nil
				}
			}
		}
		if e = s.Repo.CompleteAppleNotification(ctx, item, success); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) RunAppleBilling(ctx context.Context, logger *slog.Logger) {
	if s.AppleBilling == nil || !s.AppleBilling.Enabled() {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if e := s.ProcessAppleNotifications(ctx); e != nil && ctx.Err() == nil && logger != nil {
			logger.ErrorContext(ctx, "apple billing queue unavailable")
		}
		if e := s.ReconcileAppleSubscriptions(ctx); e != nil && ctx.Err() == nil && logger != nil {
			logger.ErrorContext(ctx, "apple subscription reconciliation unavailable")
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Reconciliation recovers missed notifications without relying on client launches.
func (s *Service) ReconcileAppleSubscriptions(ctx context.Context) error {
	if s.AppleBilling == nil || !s.AppleBilling.Enabled() || s.AppleBilling.Environment() == "Xcode" {
		return nil
	}
	chains, e := s.Repo.LeaseAppleReconciliation(ctx, s.AppleBilling.Environment())
	if e != nil {
		return e
	}
	for _, chain := range chains {
		statuses, err := s.AppleBilling.SubscriptionStatuses(ctx, chain.Original)
		if err == nil {
			for _, status := range statuses {
				if err = s.Repo.SaveAppleTransaction(ctx, 0, chain.Brand, status.Transaction, &status.Renewal); err != nil {
					break
				}
			}
		}
		if e = s.Repo.FinishAppleReconciliation(ctx, chain, err == nil); e != nil {
			return e
		}
	}
	return nil
}

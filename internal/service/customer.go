package service

import (
	"context"
	"strings"
	"time"

	"clientesFrecuentes/internal/mailer"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/web"

	"github.com/google/uuid"
)

func (s *Service) CurrentUser(ctx context.Context, actorID int64) (model.CurrentUser, error) {
	return s.Repo.GetCurrentUser(ctx, actorID)
}

func (s *Service) UpdateCurrentUser(ctx context.Context, actorID int64, expectedVersion int, req model.UpdateAccountRequest) (model.CurrentUser, error) {
	if expectedVersion < 1 || (!req.Name.Set && !req.LastName.Set && !req.Alias.Set) {
		return model.CurrentUser{}, ErrInvalidRequest
	}
	if req.Name.Set && (req.Name.Value == nil || !normalizePatch(&req.Name, 120) || *req.Name.Value == "") {
		return model.CurrentUser{}, ErrInvalidRequest
	}
	if !normalizePatch(&req.LastName, 120) || !normalizePatch(&req.Alias, 80) {
		return model.CurrentUser{}, ErrInvalidRequest
	}
	return s.Repo.UpdateAccount(ctx, actorID, expectedVersion, req)
}

func (s *Service) RequestEmailChange(ctx context.Context, actorID int64, expectedVersion int, authTime time.Time, req model.EmailRequest) error {
	if s.Config.MailProvider != "smtp" || len(s.Config.OutboxEncryptionKey) != 32 {
		return ErrEmailChangeUnavailable
	}
	if s.Now().Sub(authTime) > 15*time.Minute {
		return ErrRecentAuthRequired
	}
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return ErrInvalidRequest
	}
	token, hash, err := identityToken()
	if err != nil {
		return err
	}
	message := mailer.EmailChangeMessage(s.Config.PublicAppURL, email, token)
	return s.Repo.EnqueueEmailChange(ctx, actorID, expectedVersion, email, hash, s.Now().Add(time.Hour), message)
}

func (s *Service) ConfirmEmailChange(ctx context.Context, req model.TokenRequest) error {
	hash, err := parseIdentityToken(req.Token)
	if err != nil {
		return ErrIdentityToken
	}
	if err = s.Repo.ConfirmEmailChange(ctx, hash, s.Now()); err == repository.ErrIdentityTokenInvalid {
		return ErrIdentityToken
	}
	return err
}

func normalizePatch(value *model.OptionalString, limit int) bool {
	if !value.Set || value.Value == nil {
		return true
	}
	normalized := strings.TrimSpace(*value.Value)
	if len(normalized) > limit {
		return false
	}
	value.Value = &normalized
	return true
}

func (s *Service) ExportCurrentUser(ctx context.Context, actorID int64) (model.AccountExport, error) {
	return s.Repo.ExportAccount(ctx, actorID)
}

func (s *Service) AnonymizeCurrentUser(ctx context.Context, actorID int64, expectedVersion int, req model.AnonymizeAccountRequest) (model.Anonymization, error) {
	if req.Confirmation != "BAJA" || expectedVersion < 1 {
		return model.Anonymization{}, ErrInvalidRequest
	}
	var deletedAt time.Time
	err := retry(ctx, func() error {
		var err error
		deletedAt, err = s.Repo.AnonymizeAccount(ctx, actorID, expectedVersion, s.cancelAccountSubscriptions)
		return err
	})
	if err != nil {
		return model.Anonymization{}, err
	}
	// Apple failure must not undo account deletion. Its encrypted revocation is
	// queued in the same database transaction and retried by the worker.
	if s.AppleIdentity != nil && s.AppleIdentity.Enabled() {
		_, _ = s.ProcessAppleRevocations(ctx, 20)
	}
	// Attempt immediately; a durable queue retries storage failures.
	if s.Media != nil {
		keys, lookupErr := s.Repo.PendingProfileDeletions(ctx, 500)
		if lookupErr == nil {
			for _, key := range keys {
				if deleteErr := s.Media.Delete(ctx, key); deleteErr == nil {
					_ = s.Repo.CompleteProfileDeletion(ctx, key)
				}
			}
		}
	}
	return model.Anonymization{RequestID: uuid.NewString(), Status: "COMPLETADA", AccessRevoked: true, LedgerPreserved: false, RequestedAt: deletedAt}, nil
}

func (s *Service) cancelAccountSubscriptions(ctx context.Context, records []repository.SubscriptionRecord) error {
	if len(records) == 0 {
		return nil
	}
	// An unresolved checkout may already exist at the provider. Keep the
	// account intact until reconciliation supplies its provider ID.
	for _, record := range records {
		if record.Subscription.Status == "CREATING" || record.ProviderID == "" {
			return ErrBillingInProgress
		}
	}
	if s.Billing == nil {
		return ErrBillingUnavailable
	}
	for _, record := range records {
		key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("puntazo:account-close:"+record.ProviderID)).String()
		provider, err := s.Billing.CancelSubscription(ctx, record.ProviderID, key)
		if err != nil || provider.ID != record.ProviderID || provider.ExternalReference != record.ExternalReference || (provider.Status != "cancelled" && provider.Status != "canceled") {
			return ErrBillingProviderFailure
		}
	}
	return nil
}

func normalizeOptional(value **string, limit int) bool {
	if *value == nil {
		return true
	}
	normalized := strings.TrimSpace(**value)
	if len(normalized) > limit {
		return false
	}
	*value = &normalized
	return true
}

func (s *Service) Customer(ctx context.Context, actorID int64) (model.Customer, error) {
	u, err := s.Repo.GetCustomer(ctx, actorID)
	if err != nil {
		return model.Customer{}, err
	}
	token, _ := s.QRForUser(u.ID)
	return model.Customer{ID: u.ID, Email: u.Email, Name: u.Name, UserCode: u.UserCode, QRToken: token}, nil
}

func (s *Service) Cards(ctx context.Context, actorID int64, page, size int) ([]model.Card, web.Pagination, error) {
	items, total, err := s.Repo.ListCards(ctx, actorID, page, size)
	if err == nil && s.Media != nil {
		for i := range items {
			if items[i].BrandLogoObjectKey != "" {
				// A temporary signing failure must not hide the customer's cards;
				// the UI keeps its storefront fallback and the next live refresh retries.
				items[i].BrandLogo, _ = s.Media.SignedGet(ctx, items[i].BrandLogoObjectKey, s.Config.MediaURLTTL)
			}
			for j := range items[i].Benefits {
				if items[i].Benefits[j].ImageObjectKey != "" {
					// Benefit artwork follows the same resilient signed-URL behavior as
					// the brand logo, keeping the text reward usable during storage errors.
					items[i].Benefits[j].ImageURL, _ = s.Media.SignedGet(ctx, items[i].Benefits[j].ImageObjectKey, s.Config.MediaURLTTL)
				}
			}
			if len(items[i].Benefits) > 0 {
				// Keep the legacy singular field aligned with the first reward while
				// clients migrate to the complete benefits collection.
				items[i].Benefit = &items[i].Benefits[0]
			}
		}
	}
	return items, pagination(page, size, total), err
}

func (s *Service) CardMovements(ctx context.Context, actorID, cardID int64, page, size int) ([]model.Movement, web.Pagination, error) {
	items, total, err := s.Repo.ListCustomerMovements(ctx, actorID, cardID, page, size)
	return items, pagination(page, size, total), err
}

func (s *Service) CustomerMovements(ctx context.Context, actorID int64, page, size int) ([]model.Movement, web.Pagination, error) {
	items, total, err := s.Repo.ListAllCustomerMovements(ctx, actorID, page, size)
	return items, pagination(page, size, total), err
}

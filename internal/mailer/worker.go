package mailer

import (
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"context"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	Repo         *repository.Repository
	Sender       Sender
	Logger       *slog.Logger
	Interval     time.Duration
	PublicAppURL string
	CipherKey    []byte
	LogoStore    interface {
		SignedGet(context.Context, string, time.Duration) (string, error)
	}
}

func (w Worker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Second
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		w.flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w Worker) flush(ctx context.Context) {
	items, err := w.Repo.ClaimEmails(ctx, 20)
	if err != nil {
		w.Logger.Error("email outbox claim failed", "error", err)
		return
	}
	for _, item := range items {
		token, decryptErr := repository.DecryptOutboxToken(item, w.CipherKey)
		if decryptErr == nil {
			decryptErr = w.Repo.ValidateClaimedEmail(ctx, item, token)
		}
		if decryptErr != nil || !item.ExpiresAt.After(time.Now()) {
			if decryptErr == nil {
				decryptErr = errors.New("identity token expired")
			}
			w.Logger.Error("email outbox payload rejected", "outbox_id", item.ID, "error", decryptErr)
			_ = w.Repo.MarkEmailFailed(ctx, item.ID, item.LeaseOwner, 5, decryptErr)
			continue
		}
		var message model.EmailMessage
		switch item.Kind {
		case "VERIFY_EMAIL":
			message = VerificationMessage(w.PublicAppURL, item.To, token)
		case "RESET_PASSWORD":
			message = PasswordResetMessage(w.PublicAppURL, item.To, token)
		case "BRAND_INVITATION":
			logoURL := ""
			if w.LogoStore != nil && item.BrandLogoObjectKey != "" {
				ttl := time.Until(item.ExpiresAt)
				if ttl > 7*24*time.Hour {
					ttl = 7 * 24 * time.Hour
				}
				if ttl > 0 {
					logoURL, err = w.LogoStore.SignedGet(ctx, item.BrandLogoObjectKey, ttl)
					if err != nil {
						w.Logger.Warn("invitation logo signing failed", "outbox_id", item.ID, "error", err)
						logoURL = ""
					}
				}
			}
			message = BrandInvitationMessage(w.PublicAppURL, item.To, token, BrandInvitationDetails{BrandName: item.BrandName, Role: item.InvitationRole, Branches: item.InvitationBranchNames, BrandLogoURL: logoURL})
		default:
			_ = w.Repo.MarkEmailFailed(ctx, item.ID, item.LeaseOwner, 5, errors.New("unknown email template"))
			continue
		}
		if err = w.Sender.Send(ctx, message); err != nil {
			w.Logger.Warn("email delivery failed", "outbox_id", item.ID, "attempt", item.Attempts, "error", err)
			if markErr := w.Repo.MarkEmailFailed(ctx, item.ID, item.LeaseOwner, item.Attempts, err); markErr != nil {
				w.Logger.Error("email failure update failed", "outbox_id", item.ID, "error", markErr)
			}
			continue
		}
		if err = w.Repo.MarkEmailSent(ctx, item.ID, item.LeaseOwner); err != nil {
			w.Logger.Error("email sent update failed", "outbox_id", item.ID, "error", err)
		}
	}
}

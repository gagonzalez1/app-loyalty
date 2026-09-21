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
		ReadEmailImage(context.Context, string) ([]byte, string, error)
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
			var logoData []byte
			var logoContentType string
			if w.LogoStore != nil && item.BrandLogoObjectKey != "" {
				logoData, logoContentType, err = w.LogoStore.ReadEmailImage(ctx, item.BrandLogoObjectKey)
				if err != nil {
					w.Logger.Warn("invitation logo loading failed", "outbox_id", item.ID, "error", err)
					logoData = nil
				} else {
					logoURL = "cid:" + brandLogoContentID
				}
			}
			message = BrandInvitationMessage(w.PublicAppURL, item.To, token, BrandInvitationDetails{BrandName: item.BrandName, Role: item.InvitationRole, Branches: item.InvitationBranchNames, BrandLogoURL: logoURL})
			if len(logoData) > 0 {
				message.InlineImages = append(message.InlineImages, model.EmailInlineImage{ContentID: brandLogoContentID, Filename: "logo-comercio", ContentType: logoContentType, Data: logoData})
			}
		default:
			_ = w.Repo.MarkEmailFailed(ctx, item.ID, item.LeaseOwner, 5, errors.New("unknown email template"))
			continue
		}
		if item.Kind == "RESET_PASSWORD" || item.Kind == "BRAND_INVITATION" {
			message.InlineImages = append(message.InlineImages, model.EmailInlineImage{ContentID: mascotContentID, Filename: "mr-puntazo.png", ContentType: "image/png", Data: mascotPNG})
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

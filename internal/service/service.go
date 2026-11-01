package service

import (
	"context"
	"errors"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/challenge"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/places"
	"clientesFrecuentes/internal/repository"
)

var (
	ErrInvalidCredentials          = errors.New("invalid credentials")
	ErrInvalidRequest              = errors.New("invalid request")
	ErrRegistrationProfileRequired = errors.New("registration profile required")
	ErrAccountTypeRequired         = errors.New("account type required")
	ErrForbidden                   = errors.New("forbidden")
	ErrDemoDisabled                = errors.New("demo signup disabled")
	ErrEmailUnverified             = errors.New("email unverified")
	ErrEmailChangeUnavailable      = errors.New("email change unavailable")
	ErrIdentityToken               = errors.New("identity token invalid")
	ErrRecentAuthRequired          = errors.New("recent authentication required")
	ErrMediaTooLarge               = errors.New("media too large")
	ErrMediaType                   = errors.New("unsupported media type")
	ErrMediaUnavailable            = errors.New("media storage unavailable")
	ErrBillingUnavailable          = errors.New("billing unavailable")
	ErrBillingInProgress           = errors.New("billing checkout awaiting provider reconciliation")
	ErrTrialStartUnknown           = errors.New("first login date requires verification")
	ErrBillingRejected             = errors.New("billing provider rejected checkout")
	ErrBillingProviderFailure      = errors.New("billing provider request failed")
	ErrSubscriptionExists          = errors.New("subscription already exists")
)

type MediaStore interface {
	Put(context.Context, string, string, []byte, []byte) error
	Delete(context.Context, string) error
	SignedGet(context.Context, string, time.Duration) (string, error)
	ReadEmailImage(context.Context, string) ([]byte, string, error)
	Ready(context.Context) error
}

type Service struct {
	Challenge         *challenge.Manager
	Repo              *repository.Repository
	Tokens            *auth.Tokens
	Config            config.Config
	Now               func() time.Time
	Media             MediaStore
	VerifyGoogleToken func(context.Context, string) (auth.GoogleIdentity, error)
	Billing           BillingProvider
	Places            ReviewPlaces
	AppleIdentity     AppleIdentityProvider
	AppleBilling      AppleBillingProvider
}

type BillingProvider interface {
	CreateSubscription(context.Context, model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error)
	GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error)
	CancelSubscription(context.Context, string, string) (model.BillingSubscriptionResult, error)
}

func New(repo *repository.Repository, tokens *auth.Tokens, cfg config.Config, media ...MediaStore) *Service {
	s := &Service{Repo: repo, Tokens: tokens, Config: cfg, Now: time.Now, VerifyGoogleToken: auth.VerifyGoogleIdentity, Places: places.New(cfg.GooglePlacesAPIKey)}
	if len(media) > 0 {
		s.Media = media[0]
	}
	return s
}

func (s *Service) checkCaptcha(ctx context.Context, flow string) error {
	if !s.Config.CaptchaEnabled {
		return nil
	}
	if s.Challenge == nil {
		return challenge.ErrUnavailable
	}
	return s.Challenge.Check(ctx, flow)
}

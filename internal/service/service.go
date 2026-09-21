package service

import (
	"context"
	"errors"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
)

var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidRequest      = errors.New("invalid request")
	ErrAccountTypeRequired = errors.New("account type required")
	ErrForbidden           = errors.New("forbidden")
	ErrDemoDisabled        = errors.New("demo signup disabled")
	ErrEmailUnverified     = errors.New("email unverified")
	ErrIdentityToken       = errors.New("identity token invalid")
	ErrRecentAuthRequired  = errors.New("recent authentication required")
	ErrMediaTooLarge       = errors.New("media too large")
	ErrMediaType           = errors.New("unsupported media type")
	ErrMediaUnavailable    = errors.New("media storage unavailable")
	ErrBillingUnavailable  = errors.New("billing unavailable")
	ErrSubscriptionExists  = errors.New("subscription already exists")
)

type MediaStore interface {
	Put(context.Context, string, string, []byte, []byte) error
	Delete(context.Context, string) error
	SignedGet(context.Context, string, time.Duration) (string, error)
	ReadEmailImage(context.Context, string) ([]byte, string, error)
	Ready(context.Context) error
}

type Service struct {
	Repo              *repository.Repository
	Tokens            *auth.Tokens
	Config            config.Config
	Now               func() time.Time
	Media             MediaStore
	VerifyGoogleToken func(context.Context, string) (string, string, string, error)
	Billing           BillingProvider
}

type BillingProvider interface {
	CreateSubscription(context.Context, model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error)
	GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error)
	CancelSubscription(context.Context, string, string) (model.BillingSubscriptionResult, error)
}

func New(repo *repository.Repository, tokens *auth.Tokens, cfg config.Config, media ...MediaStore) *Service {
	s := &Service{Repo: repo, Tokens: tokens, Config: cfg, Now: time.Now, VerifyGoogleToken: auth.VerifyGoogleToken}
	if len(media) > 0 {
		s.Media = media[0]
	}
	return s
}

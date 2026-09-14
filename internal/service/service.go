package service

import (
	"context"
	"errors"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/repository"
)

var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidRequest      = errors.New("invalid request")
	ErrAccountTypeRequired = errors.New("account type required")
	ErrForbidden           = errors.New("forbidden")
	ErrDemoDisabled        = errors.New("demo signup disabled")
	ErrDemoAccess          = errors.New("demo access denied")
	ErrEmailUnverified     = errors.New("email unverified")
	ErrIdentityToken       = errors.New("identity token invalid")
	ErrRecentAuthRequired  = errors.New("recent authentication required")
	ErrMediaTooLarge       = errors.New("media too large")
	ErrMediaType           = errors.New("unsupported media type")
	ErrMediaUnavailable    = errors.New("media storage unavailable")
)

type MediaStore interface {
	Put(context.Context, string, string, []byte, []byte) error
	Delete(context.Context, string) error
	SignedGet(context.Context, string, time.Duration) (string, error)
	Ready(context.Context) error
}

type Service struct {
	Repo              *repository.Repository
	Tokens            *auth.Tokens
	Config            config.Config
	Now               func() time.Time
	Media             MediaStore
	VerifyGoogleToken func(context.Context, string) (string, string, string, error)
}

func New(repo *repository.Repository, tokens *auth.Tokens, cfg config.Config, media ...MediaStore) *Service {
	s := &Service{Repo: repo, Tokens: tokens, Config: cfg, Now: time.Now, VerifyGoogleToken: auth.VerifyGoogleToken}
	if len(media) > 0 {
		s.Media = media[0]
	}
	return s
}

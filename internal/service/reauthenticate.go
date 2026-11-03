package service

import (
	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/model"
	passwordwork "clientesFrecuentes/internal/password"
	"clientesFrecuentes/internal/repository"
	"context"
	"errors"
	"strings"
)

func (s *Service) LinkGoogle(ctx context.Context, userID int64, sessionID string, version int, req model.GoogleLinkRequest) (model.AuthData, error) {
	return s.renewAuthentication(ctx, userID, sessionID, version, req.Password, req.IDToken, true)
}
func (s *Service) Reauthenticate(ctx context.Context, userID int64, sessionID string, version int, req model.ReauthenticateRequest) (model.AuthData, error) {
	if req.Apple != nil {
		if req.Password != "" || req.IDToken != "" {
			return model.AuthData{}, ErrInvalidRequest
		}
		return s.renewAppleAuthentication(ctx, userID, sessionID, version, req.Version, *req.Apple, false)
	}
	if (req.Password == "") == (req.IDToken == "") {
		return model.AuthData{}, ErrInvalidRequest
	}
	return s.renewAuthentication(ctx, userID, sessionID, version, req.Password, req.IDToken, false)
}
func (s *Service) renewAuthentication(ctx context.Context, userID int64, sessionID string, version int, password, idToken string, link bool) (model.AuthData, error) {
	u, err := s.Repo.GetAuthUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.AuthData{}, ErrInvalidCredentials
		}
		return model.AuthData{}, err
	}
	if password != "" {
		if u.PasswordHash == nil || len(password) > 72 {
			return model.AuthData{}, ErrInvalidCredentials
		}
		if err = passwordwork.Compare(ctx, []byte(*u.PasswordHash), []byte(password)); err != nil {
			if errors.Is(err, passwordwork.ErrBusy) {
				return model.AuthData{}, err
			}
			return model.AuthData{}, ErrInvalidCredentials
		}
	} else if link {
		return model.AuthData{}, ErrInvalidCredentials
	}
	subject, email := "", ""
	if link || idToken != "" {
		if strings.TrimSpace(idToken) == "" {
			return model.AuthData{}, ErrInvalidRequest
		}
		verify := s.VerifyGoogleToken
		if verify == nil {
			verify = auth.VerifyGoogleIdentity
		}
		identity, e := verify(ctx, idToken)
		if e != nil {
			return model.AuthData{}, ErrInvalidCredentials
		}
		subject, email = identity.GoogleID, strings.ToLower(strings.TrimSpace(identity.Email))
		if subject == "" || (link && !strings.EqualFold(email, strings.TrimSpace(u.User.Email))) {
			return model.AuthData{}, ErrInvalidCredentials
		}
		if !link && (u.GoogleID == nil || *u.GoogleID != subject) {
			return model.AuthData{}, ErrInvalidCredentials
		}
	}
	credentials, err := newSessionCredentials()
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.AuthData{}, ErrInvalidCredentials
		}
		return model.AuthData{}, err
	}
	updated, err := s.Repo.RenewAuthentication(ctx, u, version, sessionID, subject, email, link, credentials.id, credentials.hash, credentials.expiresAt, credentials.authTime)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.AuthData{}, ErrInvalidCredentials
		}
		return model.AuthData{}, err
	}
	session, err := s.session(updated, credentials)
	return model.AuthData{Session: session, User: updated}, err
}

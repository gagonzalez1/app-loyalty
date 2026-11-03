package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"clientesFrecuentes/internal/appleidentity"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"github.com/google/uuid"
)

var ErrAppleAuthUnavailable = errors.New("apple authentication unavailable")

type AppleIdentityProvider interface {
	Enabled() bool
	Verify(context.Context, string, []byte) (appleidentity.Identity, error)
	Exchange(context.Context, string, appleidentity.Identity, []byte) (appleidentity.Tokens, error)
	Revoke(context.Context, string, string) error
}

func (s *Service) appleIdentityReady() bool {
	return s.AppleIdentity != nil && s.AppleIdentity.Enabled() && s.Repo != nil && len(s.Repo.OutboxCipherKey) == 32
}

func (s *Service) CreateAppleChallenge(ctx context.Context, purpose string, userID int64, sessionID string) (model.AppleChallenge, error) {
	if !s.appleIdentityReady() {
		return model.AppleChallenge{}, ErrAppleAuthUnavailable
	}
	if purpose != "login" && purpose != "link" && purpose != "reauthenticate" {
		return model.AppleChallenge{}, ErrInvalidRequest
	}
	if purpose == "login" {
		userID = 0
		sessionID = ""
	} else if userID <= 0 || sessionID == "" {
		return model.AppleChallenge{}, ErrInvalidCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return model.AppleChallenge{}, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(nonce))
	now := s.Now().UTC()
	out := model.AppleChallenge{ChallengeID: uuid.NewString(), Nonce: nonce, ExpiresAt: now.Add(5 * time.Minute)}
	err := s.Repo.CreateAppleChallenge(ctx, repository.AppleChallengeRecord{ID: out.ChallengeID, Purpose: purpose, UserID: userID, SessionID: sessionID, NonceHash: hash[:], ExpiresAt: out.ExpiresAt}, now)
	return out, err
}

func (s *Service) appleProof(ctx context.Context, proof model.AppleAuthProof, purpose string, userID int64, sessionID string) (repository.AppleChallengeRecord, appleidentity.Identity, error) {
	if !s.appleIdentityReady() {
		return repository.AppleChallengeRecord{}, appleidentity.Identity{}, ErrAppleAuthUnavailable
	}
	if proof.IDToken == "" || len(proof.IDToken) > 16384 || proof.AuthorizationCode == "" || len(proof.AuthorizationCode) > 4096 {
		return repository.AppleChallengeRecord{}, appleidentity.Identity{}, ErrInvalidRequest
	}
	challenge, err := s.Repo.AppleChallenge(ctx, proof.ChallengeID, purpose, userID, sessionID, s.Now())
	if err != nil {
		return challenge, appleidentity.Identity{}, appleAuthError(err)
	}
	identity, err := s.AppleIdentity.Verify(ctx, proof.IDToken, challenge.NonceHash)
	return challenge, identity, appleAuthError(err)
}

func appleAuthError(err error) error {
	if errors.Is(err, repository.ErrAppleChallenge) || errors.Is(err, appleidentity.ErrInvalidToken) || errors.Is(err, repository.ErrNotFound) {
		return ErrInvalidCredentials
	}
	if errors.Is(err, appleidentity.ErrUnavailable) || errors.Is(err, appleidentity.ErrProvider) || errors.Is(err, appleidentity.ErrCipher) {
		return ErrAppleAuthUnavailable
	}
	return err
}

func (s *Service) LoginApple(ctx context.Context, req model.AppleAuthRequest) (model.AuthData, error) {
	challenge, identity, err := s.appleProof(ctx, req.AppleAuthProof, "login", 0, "")
	if err != nil {
		return model.AuthData{}, err
	}
	u, err := s.Repo.ResolveAppleUser(ctx, identity.Subject, identity.Email)
	var signup *repository.AppleSignup
	if errors.Is(err, repository.ErrNotFound) {
		signup, err = s.appleSignup(ctx, req, identity)
	}
	if err != nil {
		return model.AuthData{}, err
	}
	// Profile/type errors above deliberately leave Apple's single-use code and
	// our challenge untouched. Only a complete request may exchange the code.
	tokens, err := s.AppleIdentity.Exchange(ctx, req.AuthorizationCode, identity, challenge.NonceHash)
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	credentials, err := newSessionCredentials()
	if err != nil {
		return model.AuthData{}, err
	}
	credentials.authTime = s.Now().UTC()
	credentials.expiresAt = credentials.authTime.Add(refreshLifetime)
	var created bool
	err = retryRegistration(ctx, func() error {
		var e error
		u, created, e = s.Repo.CompleteAppleLogin(ctx, challenge, identity, tokens, signup, appleSession(credentials))
		return e
	})
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	session, err := s.session(u, credentials)
	return model.AuthData{Session: session, User: u, AccountCreated: created}, err
}

func (s *Service) appleSignup(ctx context.Context, req model.AppleAuthRequest, identity appleidentity.Identity) (*repository.AppleSignup, error) {
	accountType := req.AccountType
	if req.TipoCuenta != nil {
		if accountType != nil && strings.TrimSpace(*accountType) != strings.TrimSpace(*req.TipoCuenta) {
			return nil, ErrInvalidRequest
		}
		accountType = req.TipoCuenta
	}
	if accountType == nil || strings.TrimSpace(*accountType) == "" {
		return nil, &SignupProfileError{Name: req.Name, LastName: req.LastName, AccountTypeRequired: true}
	}
	kind := strings.TrimSpace(*accountType)
	if kind != "CLIENTE_FINAL" && kind != "PERSONAL_MARCA" {
		return nil, ErrInvalidRequest
	}
	if !s.Config.DemoSignupEnabled {
		return nil, ErrDemoDisabled
	}
	if _, err := normalizeEmail(identity.Email); err != nil || !identity.EmailVerified {
		return nil, ErrInvalidCredentials
	}
	name, lastName, err := registrationNames(req.Name, req.LastName)
	if err != nil {
		return nil, &SignupProfileError{Name: req.Name, LastName: req.LastName}
	}
	if err = s.checkCaptcha(ctx, "signup"); err != nil {
		return nil, err
	}
	signup := &repository.AppleSignup{Name: name, LastName: lastName, AccountType: kind, FinalQRHash: func(id int64) []byte { _, hash := s.QRForUser(id); return hash }}
	if kind == "CLIENTE_FINAL" {
		if req.MerchantRegistration != nil {
			return nil, ErrInvalidRequest
		}
	} else {
		registration, e := s.validateGoogleMerchant(req.MerchantRegistration)
		if e != nil {
			return nil, e
		}
		signup.Merchant = &registration
	}
	return signup, nil
}

func appleSession(credentials sessionCredentials) repository.AppleSession {
	return repository.AppleSession{ID: credentials.id, RefreshHash: credentials.hash, ExpiresAt: credentials.expiresAt, AuthTime: credentials.authTime}
}

func (s *Service) LinkApple(ctx context.Context, userID int64, sessionID string, authVersion int, authTime time.Time, req model.AppleLinkRequest) (model.AuthData, error) {
	now := s.Now()
	if authTime.IsZero() || authTime.Before(now.Add(-10*time.Minute)) || authTime.After(now.Add(time.Minute)) {
		return model.AuthData{}, ErrRecentAuthRequired
	}
	if req.Version < 1 {
		return model.AuthData{}, ErrInvalidRequest
	}
	return s.renewAppleAuthentication(ctx, userID, sessionID, authVersion, req.Version, req.AppleAuthProof, true)
}

func (s *Service) renewAppleAuthentication(ctx context.Context, userID int64, sessionID string, authVersion, accountVersion int, proof model.AppleAuthProof, link bool) (model.AuthData, error) {
	purpose := "reauthenticate"
	if link {
		purpose = "link"
	}
	challenge, identity, err := s.appleProof(ctx, proof, purpose, userID, sessionID)
	if err != nil {
		return model.AuthData{}, err
	}
	u, err := s.Repo.GetAuthUserByID(ctx, userID)
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	if authVersion != u.User.AuthVersion {
		return model.AuthData{}, ErrInvalidCredentials
	}
	if accountVersion != 0 && accountVersion != u.User.Version {
		return model.AuthData{}, repository.ErrPreconditionFailed
	}
	if !link {
		subject, e := s.Repo.AppleSubjectForUser(ctx, userID)
		if e != nil || subject != identity.Subject {
			return model.AuthData{}, ErrInvalidCredentials
		}
	} else if subject, e := s.Repo.AppleSubjectForUser(ctx, userID); e == nil && subject != identity.Subject {
		return model.AuthData{}, repository.ErrAppleIdentityConflict
	} else if e != nil && !errors.Is(e, repository.ErrNotFound) {
		return model.AuthData{}, e
	}
	tokens, err := s.AppleIdentity.Exchange(ctx, proof.AuthorizationCode, identity, challenge.NonceHash)
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	credentials, err := newSessionCredentials()
	if err != nil {
		return model.AuthData{}, err
	}
	credentials.authTime = s.Now().UTC()
	credentials.expiresAt = credentials.authTime.Add(refreshLifetime)
	updated, err := s.Repo.CompleteAppleAuthentication(ctx, challenge, identity, tokens, userID, sessionID, authVersion, accountVersion, link, appleSession(credentials))
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	session, err := s.session(updated, credentials)
	return model.AuthData{Session: session, User: updated}, err
}

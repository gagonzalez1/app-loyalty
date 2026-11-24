package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"time"

	"clientesFrecuentes/internal/appleidentity"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrAppleAuthCancelled = errors.New("apple authentication cancelled")
	ErrAppleAuthPending   = errors.New("apple authentication pending")
	appleBrowserVerifier  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
)

func (s *Service) appleBrowserReady() bool {
	return s.Config.AppleBrowserAuthEnabled && s.AppleBrowser != nil && s.AppleBrowser.Enabled() &&
		s.Repo != nil && len(s.Repo.OutboxCipherKey) == 32
}

func (s *Service) StartAppleBrowser(ctx context.Context, req model.AppleBrowserStartRequest, platform string, userID int64, sessionID string, authVersion int) (model.AppleBrowserStart, error) {
	if !s.appleBrowserReady() {
		return model.AppleBrowserStart{}, ErrAppleAuthUnavailable
	}
	verifierHash, err := base64.RawURLEncoding.Strict().DecodeString(req.CodeChallenge)
	if err != nil || len(verifierHash) != sha256.Size || len(req.CodeChallenge) != 43 {
		return model.AppleBrowserStart{}, ErrInvalidRequest
	}
	if (req.Purpose == "login" && userID != 0) || (req.Purpose == "reauthenticate" && (userID < 1 || sessionID == "" || authVersion < 1)) ||
		(req.Purpose != "login" && req.Purpose != "reauthenticate") {
		return model.AppleBrowserStart{}, ErrInvalidRequest
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return model.AppleBrowserStart{}, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	nonceHash := sha256.Sum256([]byte(nonce))
	now := s.Now().UTC()
	f := repository.AppleBrowserFlow{
		ID: uuid.NewString(), Platform: platform, UserID: userID, AuthVersion: authVersion,
		NonceHash: nonceHash[:], VerifierHash: verifierHash, ExpiresAt: now.Add(5 * time.Minute),
	}
	if err = s.Repo.CreateAppleBrowserFlow(ctx, f, sessionID, now); err != nil {
		return model.AppleBrowserStart{}, appleAuthError(err)
	}
	query := url.Values{
		"client_id": {s.Config.AppleBrowserClientID}, "redirect_uri": {s.Config.AppleBrowserRedirectURI},
		"response_type": {"code id_token"}, "response_mode": {"form_post"}, "state": {f.ID}, "nonce": {nonce},
	}
	return model.AppleBrowserStart{
		FlowID: f.ID, AuthorizationURL: "https://appleid.apple.com/auth/authorize?" + query.Encode(), ExpiresAt: f.ExpiresAt,
	}, nil
}

type appleBrowserProof struct {
	Code    string
	IDToken string
}

func (s *Service) ReceiveAppleBrowserCallback(ctx context.Context, state, code, idToken, providerError string) (string, error) {
	if !s.appleBrowserReady() {
		return "", ErrAppleAuthUnavailable
	}
	if id, err := uuid.Parse(state); err != nil || id.String() != state {
		return "", ErrInvalidRequest
	}
	status := "ready"
	var proof, nonce []byte
	if providerError != "" {
		if code != "" || idToken != "" {
			return "", ErrInvalidRequest
		}
		status = "failed"
		if providerError == "user_cancelled_authorize" || providerError == "access_denied" {
			status = "cancelled"
		}
	} else {
		if code == "" || len(code) > 4096 || idToken == "" || len(idToken) > 16384 {
			return "", ErrInvalidRequest
		}
		raw, err := json.Marshal(appleBrowserProof{Code: code, IDToken: idToken})
		if err != nil {
			return "", err
		}
		proof, nonce, err = appleidentity.SealBrowserProof(s.Repo.OutboxCipherKey, state, raw)
		if err != nil {
			return "", appleAuthError(err)
		}
	}
	platform, err := s.Repo.ReceiveAppleBrowserProof(ctx, state, status, proof, nonce, s.Now())
	if err != nil {
		return "", appleAuthError(err)
	}
	query := url.Values{"flow_id": {state}}
	target := url.URL{Scheme: "puntazo", Host: "apple-auth-return"}
	if platform == "web" {
		origin, err := url.Parse(s.Config.PublicAppURL)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil {
			return "", ErrAppleAuthUnavailable
		}
		target = url.URL{Scheme: origin.Scheme, Host: origin.Host, Path: "/apple-auth-return"}
	}
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func (s *Service) FinishAppleBrowser(ctx context.Context, req model.AppleBrowserFinishRequest, platform string, userID int64, sessionID string, authVersion int) (model.AuthData, error) {
	if !s.appleBrowserReady() {
		return model.AuthData{}, ErrAppleAuthUnavailable
	}
	if id, err := uuid.Parse(req.FlowID); err != nil || id.String() != req.FlowID || !appleBrowserVerifier.MatchString(req.CodeVerifier) {
		return model.AuthData{}, ErrInvalidRequest
	}
	verifierHash := sha256.Sum256([]byte(req.CodeVerifier))
	f, err := s.Repo.ClaimAppleBrowserFlow(ctx, req.FlowID, platform, userID, sessionID, authVersion, verifierHash[:], s.Now())
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	switch f.Status {
	case "pending":
		return model.AuthData{}, ErrAppleAuthPending
	case "cancelled":
		return model.AuthData{}, ErrAppleAuthCancelled
	case "failed":
		return model.AuthData{}, ErrAppleAuthUnavailable
	}

	raw, err := appleidentity.OpenBrowserProof(s.Repo.OutboxCipherKey, req.FlowID, f.Proof, f.ProofNonce)
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	var proof appleBrowserProof
	if err = json.Unmarshal(raw, &proof); err != nil {
		return model.AuthData{}, ErrAppleAuthUnavailable
	}
	identity, err := s.AppleBrowser.Verify(ctx, proof.IDToken, f.NonceHash)
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	if userID != 0 {
		subject, err := s.Repo.AppleSubjectForUser(ctx, userID)
		if err != nil {
			return model.AuthData{}, appleAuthError(err)
		}
		if subject != identity.Subject {
			return model.AuthData{}, ErrInvalidCredentials
		}
	} else if _, err = s.Repo.ResolveAppleUser(ctx, identity.Subject, ""); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			err = repository.ErrAppleLinkRequired
		}
		return model.AuthData{}, err
	}
	tokens, err := s.AppleBrowser.Exchange(ctx, proof.Code, identity, f.NonceHash)
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	credentials, err := newSessionCredentials()
	if err != nil {
		return model.AuthData{}, err
	}
	credentials.authTime = s.Now().UTC()
	credentials.expiresAt = credentials.authTime.Add(refreshLifetime)
	u, err := s.Repo.CompleteAppleBrowserAuth(ctx, identity.Subject, tokens, userID, sessionID, authVersion, req.Version, appleSession(credentials))
	if err != nil {
		return model.AuthData{}, appleAuthError(err)
	}
	session, err := s.session(u, credentials)
	return model.AuthData{Session: session, User: u}, err
}

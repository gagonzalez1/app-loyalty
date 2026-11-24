package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrGoogleBrowserUnavailable = errors.New("google browser authentication unavailable")
	ErrGoogleBrowserCancelled   = errors.New("google browser authentication cancelled")
	ErrGoogleBrowserInvalid     = errors.New("google browser authentication invalid")
	googleBrowserVerifier       = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
)

const googleBrowserLifetime = 5 * time.Minute

func (s *Service) GoogleBrowserEnabled() bool {
	return s != nil && s.Config.GoogleBrowserClientID != "" && s.Config.GoogleBrowserRedirectURI != "" &&
		s.Repo != nil && s.Repo.Pool != nil && len(s.Repo.OutboxCipherKey) == 32 && s.VerifyGoogleBrowserToken != nil
}

func (s *Service) StartGoogleBrowser(ctx context.Context, req model.GoogleBrowserStartRequest) (model.GoogleBrowserStart, error) {
	if !s.GoogleBrowserEnabled() {
		return model.GoogleBrowserStart{}, ErrGoogleBrowserUnavailable
	}
	verifierHash, err := base64.RawURLEncoding.Strict().DecodeString(req.CodeChallenge)
	if len(req.CodeChallenge) != 43 || len(verifierHash) != sha256.Size || err != nil {
		return model.GoogleBrowserStart{}, ErrGoogleBrowserInvalid
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return model.GoogleBrowserStart{}, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	nonceHash := sha256.Sum256([]byte(nonce))
	id := uuid.NewString()
	expiresAt := s.Now().UTC().Add(googleBrowserLifetime)
	if err = s.Repo.CreateGoogleBrowserFlow(ctx, id, nonceHash[:], verifierHash, expiresAt); err != nil {
		return model.GoogleBrowserStart{}, err
	}
	query := url.Values{
		"client_id": {s.Config.GoogleBrowserClientID}, "redirect_uri": {s.Config.GoogleBrowserRedirectURI},
		"response_type": {"id_token"}, "response_mode": {"form_post"}, "scope": {"openid email profile"},
		"prompt": {"select_account"}, "state": {id}, "nonce": {nonce},
	}
	return model.GoogleBrowserStart{
		FlowID: id, AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth?" + query.Encode(), ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) ReceiveGoogleBrowserCallback(ctx context.Context, state, idToken, providerError string) (string, error) {
	if !s.GoogleBrowserEnabled() {
		return "", ErrGoogleBrowserUnavailable
	}
	if !validGoogleBrowserID(state) || (idToken == "") == (providerError == "") || len(idToken) > 16<<10 || len(providerError) > 128 {
		return "", ErrGoogleBrowserInvalid
	}
	origin, err := url.Parse(s.Config.PublicAppURL)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil {
		return "", ErrGoogleBrowserUnavailable
	}
	status := "ready"
	var proof []byte
	if providerError != "" {
		status = "failed"
		if providerError == "access_denied" {
			status = "cancelled"
		}
	} else {
		nonceHash, err := s.Repo.PendingGoogleBrowserNonce(ctx, state, s.Now())
		if err != nil {
			return "", googleBrowserError(err)
		}
		if err = s.VerifyGoogleBrowserToken(ctx, idToken, s.Config.GoogleBrowserClientID, nonceHash); err != nil {
			return "", ErrGoogleBrowserInvalid
		}
		gcm, err := googleBrowserCipher(s.Repo.OutboxCipherKey)
		if err != nil {
			return "", ErrGoogleBrowserUnavailable
		}
		proof = gcm.Seal(nil, nil, []byte(idToken), googleBrowserAAD(state))
	}
	if err = s.Repo.ReceiveGoogleBrowserProof(ctx, state, status, proof, s.Now()); err != nil {
		return "", googleBrowserError(err)
	}
	target := url.URL{Scheme: origin.Scheme, Host: origin.Host, Path: "/login", RawQuery: url.Values{"google_flow_id": {state}}.Encode()}
	return target.String(), nil
}

func (s *Service) FinishGoogleBrowser(ctx context.Context, req model.GoogleBrowserFinishRequest) (model.GoogleBrowserFinish, error) {
	if !s.GoogleBrowserEnabled() {
		return model.GoogleBrowserFinish{}, ErrGoogleBrowserUnavailable
	}
	if !validGoogleBrowserID(req.FlowID) || !googleBrowserVerifier.MatchString(req.CodeVerifier) {
		return model.GoogleBrowserFinish{}, ErrGoogleBrowserInvalid
	}
	verifierHash := sha256.Sum256([]byte(req.CodeVerifier))
	status, proof, err := s.Repo.ClaimGoogleBrowserFlow(ctx, req.FlowID, verifierHash[:], s.Now())
	if err != nil {
		return model.GoogleBrowserFinish{}, googleBrowserError(err)
	}
	if status == "cancelled" {
		return model.GoogleBrowserFinish{}, ErrGoogleBrowserCancelled
	}
	if status != "ready" {
		return model.GoogleBrowserFinish{}, ErrGoogleBrowserUnavailable
	}
	gcm, err := googleBrowserCipher(s.Repo.OutboxCipherKey)
	if err != nil {
		return model.GoogleBrowserFinish{}, ErrGoogleBrowserUnavailable
	}
	raw, err := gcm.Open(nil, nil, proof, googleBrowserAAD(req.FlowID))
	if err != nil {
		return model.GoogleBrowserFinish{}, ErrGoogleBrowserUnavailable
	}
	return model.GoogleBrowserFinish{IDToken: string(raw)}, nil
}

func googleBrowserError(err error) error {
	if errors.Is(err, repository.ErrGoogleBrowserFlow) {
		return ErrGoogleBrowserInvalid
	}
	return err
}

func validGoogleBrowserID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func googleBrowserCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func googleBrowserAAD(id string) []byte {
	return []byte("puntazo:google-browser-proof:v1\x00" + id)
}

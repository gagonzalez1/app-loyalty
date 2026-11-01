package repository_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clientesFrecuentes/internal/appleidentity"
	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"github.com/google/uuid"
)

type testAppleIdentityProvider struct {
	identity    appleidentity.Identity
	exchanges   atomic.Int32
	revocations atomic.Int32
	failRevoke  atomic.Bool
}

func (*testAppleIdentityProvider) Enabled() bool { return true }
func (p *testAppleIdentityProvider) Verify(_ context.Context, token string, hash []byte) (appleidentity.Identity, error) {
	if !strings.HasPrefix(token, "test-identity:") {
		return appleidentity.Identity{}, appleidentity.ErrInvalidToken
	}
	nonce := strings.TrimPrefix(token, "test-identity:")
	digest := sha256.Sum256([]byte(nonce))
	if !bytes.Equal(digest[:], hash) {
		return appleidentity.Identity{}, appleidentity.ErrInvalidToken
	}
	return p.identity, nil
}
func (p *testAppleIdentityProvider) Exchange(_ context.Context, code string, _ appleidentity.Identity, _ []byte) (appleidentity.Tokens, error) {
	if code != "test-authorization-code" {
		return appleidentity.Tokens{}, appleidentity.ErrInvalidToken
	}
	p.exchanges.Add(1)
	return appleidentity.Tokens{AccessToken: "sensitive-access-token", RefreshToken: "sensitive-refresh-token", ClientID: "com.puntazo.test"}, nil
}
func (p *testAppleIdentityProvider) Revoke(_ context.Context, token, kind string) error {
	p.revocations.Add(1)
	if token != "sensitive-refresh-token" || kind != "refresh_token" {
		return appleidentity.ErrInvalidToken
	}
	if p.failRevoke.Load() {
		return appleidentity.ErrProvider
	}
	return nil
}

func appleTestService(t *testing.T) (*service.Service, *repository.Repository, *testAppleIdentityProvider) {
	t.Helper()
	pool := reviewsDB(t)
	applyReviews(t, pool)
	repo := repository.New(pool, bytes.Repeat([]byte{0x42}, 32))
	svc := service.New(repo, auth.NewTokens("12345678901234567890123456789012", "apple-integration"), config.Config{DemoSignupEnabled: true})
	provider := &testAppleIdentityProvider{identity: appleidentity.Identity{Subject: "apple-subject", Email: "hidden@privaterelay.appleid.com", EmailVerified: true, PrivateRelay: true}}
	svc.AppleIdentity = provider
	return svc, repo, provider
}

func appleProofFor(t *testing.T, svc *service.Service, purpose string, userID int64, sessionID string) model.AppleAuthProof {
	t.Helper()
	challenge, err := svc.CreateAppleChallenge(context.Background(), purpose, userID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return model.AppleAuthProof{ChallengeID: challenge.ChallengeID, IDToken: "test-identity:" + challenge.Nonce, AuthorizationCode: "test-authorization-code"}
}

func appleSignupRequest(proof model.AppleAuthProof) model.AppleAuthRequest {
	kind := "CLIENTE_FINAL"
	return model.AppleAuthRequest{AppleAuthProof: proof, Name: "Apple", LastName: "Customer", AccountType: &kind}
}

func TestPostgresAppleSignupProfileNonceAndEncryptedExport(t *testing.T) {
	svc, repo, provider := appleTestService(t)
	ctx := context.Background()
	proof := appleProofFor(t, svc, "login", 0, "")
	_, err := svc.LoginApple(ctx, model.AppleAuthRequest{AppleAuthProof: proof})
	if !errors.Is(err, service.ErrAccountTypeRequired) {
		t.Fatal("missing type error", err)
	}
	request := appleSignupRequest(proof)
	request.LastName = ""
	if _, err = svc.LoginApple(ctx, request); !errors.Is(err, service.ErrRegistrationProfileRequired) {
		t.Fatal("missing profile error", err)
	}
	if provider.exchanges.Load() != 0 {
		t.Fatal("incomplete signup consumed Apple's code")
	}
	request = appleSignupRequest(proof)
	data, err := svc.LoginApple(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !data.AccountCreated || data.User.ID == 0 || !data.User.EmailVerified {
		t.Fatal(data.User)
	}
	if _, err = svc.LoginApple(ctx, request); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatal("challenge replay", err)
	}
	if provider.exchanges.Load() != 1 {
		t.Fatal("replay exchanged code")
	}
	current, err := repo.GetCurrentUser(ctx, data.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !current.AuthMethods.Apple || current.AuthMethods.Google || current.AuthMethods.Password {
		t.Fatal(current.AuthMethods)
	}
	exported, err := repo.ExportAccount(ctx, data.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exported.AppleIdentity == nil || exported.AppleIdentity.Subject != provider.identity.Subject || !exported.AppleIdentity.PrivateRelay {
		t.Fatal(exported.AppleIdentity)
	}
	raw, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("sensitive-")) || bytes.Contains(raw, []byte("token_ciphertext")) {
		t.Fatal("export leaked provider credentials")
	}
	var cipher, nonce []byte
	var tokenType, clientID string
	if err = repo.Pool.QueryRow(ctx, `SELECT token_ciphertext,token_nonce,token_type,client_id FROM apple_identities WHERE usuario_id=$1`, data.User.ID).Scan(&cipher, &nonce, &tokenType, &clientID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte("sensitive")) {
		t.Fatal("plaintext credentials stored")
	}
	token, err := appleidentity.DecryptToken(repo.OutboxCipherKey, clientID, tokenType, cipher, nonce)
	if err != nil || token != "sensitive-refresh-token" {
		t.Fatal(err)
	}
	// Subject remains authoritative when Apple changes its returned email.
	provider.identity.Email = "changed@privaterelay.appleid.com"
	again, err := svc.LoginApple(ctx, model.AppleAuthRequest{AppleAuthProof: appleProofFor(t, svc, "login", 0, "")})
	if err != nil || again.User.ID != data.User.ID || again.AccountCreated {
		t.Fatal(again.User, err)
	}
	provider.identity.Email = ""
	provider.identity.PrivateRelay = false
	if _, err = svc.LoginApple(ctx, model.AppleAuthRequest{AppleAuthProof: appleProofFor(t, svc, "login", 0, "")}); err != nil {
		t.Fatal("existing subject requires first-consent email again", err)
	}
	exported, err = repo.ExportAccount(ctx, data.User.ID)
	if err != nil || exported.AppleIdentity.Email != "changed@privaterelay.appleid.com" || !exported.AppleIdentity.PrivateRelay {
		t.Fatal("missing email erased retained Apple metadata", exported.AppleIdentity, err)
	}
}

func TestPostgresAppleExplicitRelayLinkAndReauthentication(t *testing.T) {
	svc, repo, provider := appleTestService(t)
	ctx := context.Background()
	var userID int64
	if err := repo.Pool.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,email_verified_at) VALUES('original@example.test','Original','PERSONAL_MARCA',now()) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	// Email matches an existing user but never creates or links an identity.
	provider.identity.Email = "original@example.test"
	if _, err := svc.LoginApple(ctx, appleSignupRequest(appleProofFor(t, svc, "login", 0, ""))); !errors.Is(err, repository.ErrAppleLinkRequired) {
		t.Fatal(err)
	}
	if provider.exchanges.Load() != 0 {
		t.Fatal("implicit link consumed code")
	}
	provider.identity.Email = "hidden@privaterelay.appleid.com"
	initial, err := repo.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.NewString()
	now := time.Now().UTC()
	if err = repo.CreateSession(ctx, sessionID, userID, bytes.Repeat([]byte{1}, 32), now.Add(time.Hour), now, initial.AuthVersion); err != nil {
		t.Fatal(err)
	}
	proof := appleProofFor(t, svc, "link", userID, sessionID)
	if _, err = svc.LinkApple(ctx, userID, sessionID, initial.AuthVersion, now.Add(-11*time.Minute), model.AppleLinkRequest{AppleAuthProof: proof, Version: initial.Version}); !errors.Is(err, service.ErrRecentAuthRequired) {
		t.Fatal(err)
	}
	if _, err = svc.LinkApple(ctx, userID, sessionID, initial.AuthVersion, now, model.AppleLinkRequest{AppleAuthProof: proof, Version: initial.Version + 1}); !errors.Is(err, repository.ErrPreconditionFailed) {
		t.Fatal(err)
	}
	linked, err := svc.LinkApple(ctx, userID, sessionID, initial.AuthVersion, now, model.AppleLinkRequest{AppleAuthProof: proof, Version: initial.Version})
	if err != nil {
		t.Fatal(err)
	}
	if linked.User.ID != userID || linked.User.Email != "original@example.test" || linked.User.AuthVersion != initial.AuthVersion+1 {
		t.Fatal("link changed account", linked.User)
	}
	if _, err = repo.ActiveSessionAccountType(ctx, userID, sessionID, initial.AuthVersion); err == nil {
		t.Fatal("old session survived link")
	}
	_, _, newSession, authVersion, _, err := svc.Tokens.ParseSessionContext(linked.Session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	reauthProof := appleProofFor(t, svc, "reauthenticate", userID, newSession)
	if _, err = svc.Reauthenticate(ctx, userID, newSession, authVersion, model.ReauthenticateRequest{Password: "must-not-mix", Apple: &reauthProof}); !errors.Is(err, service.ErrInvalidRequest) {
		t.Fatal(err)
	}
	// A login-purpose nonce cannot prove recent authentication.
	wrongPurpose := appleProofFor(t, svc, "login", 0, "")
	if _, err = svc.Reauthenticate(ctx, userID, newSession, authVersion, model.ReauthenticateRequest{Apple: &wrongPurpose}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatal(err)
	}
	renewed, err := svc.Reauthenticate(ctx, userID, newSession, authVersion, model.ReauthenticateRequest{Apple: &reauthProof, Version: linked.User.Version})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.User.AuthVersion != authVersion+1 {
		t.Fatal("reauth did not rotate fence")
	}
	if _, err = repo.ActiveSessionAccountType(ctx, userID, newSession, authVersion); err == nil {
		t.Fatal("old session survived reauth")
	}
	_, _, activeSession, activeVersion, _, _ := svc.Tokens.ParseSessionContext(renewed.Session.AccessToken)
	provider.identity.Subject = "unrelated-apple-account"
	wrongIdentity := appleProofFor(t, svc, "reauthenticate", userID, activeSession)
	before := provider.exchanges.Load()
	if _, err = svc.Reauthenticate(ctx, userID, activeSession, activeVersion, model.ReauthenticateRequest{Apple: &wrongIdentity, Version: renewed.User.Version}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatal("different Apple identity reauthenticated existing account", err)
	}
	if provider.exchanges.Load() != before {
		t.Fatal("wrong reauthentication subject consumed Apple code")
	}
}

func TestPostgresAppleDeletionDurablyRevokesWithoutBlocking(t *testing.T) {
	svc, repo, provider := appleTestService(t)
	ctx := context.Background()
	data, err := svc.LoginApple(ctx, appleSignupRequest(appleProofFor(t, svc, "login", 0, "")))
	if err != nil {
		t.Fatal(err)
	}
	provider.failRevoke.Store(true)
	if _, err = svc.AnonymizeCurrentUser(ctx, data.User.ID, data.User.Version, model.AnonymizeAccountRequest{Confirmation: "BAJA"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = repo.Pool.QueryRow(ctx, `SELECT count(*) FROM apple_identities WHERE usuario_id=$1`, data.User.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("identity retained", count, err)
	}
	if err = repo.Pool.QueryRow(ctx, `SELECT count(*) FROM apple_identity_revocations WHERE attempts=1 AND last_error='provider revocation pending'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("durable retry missing", count, err)
	}
	if _, err = repo.GetAuthUserByID(ctx, data.User.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("deleted account can authenticate", err)
	}
	if _, err = repo.Pool.Exec(ctx, `UPDATE apple_identity_revocations SET available_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	provider.failRevoke.Store(false)
	completed, err := svc.ProcessAppleRevocations(ctx, 20)
	if err != nil || completed != 1 {
		t.Fatal(completed, err)
	}
	if err = repo.Pool.QueryRow(ctx, `SELECT count(*) FROM apple_identity_revocations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked token retained", count, err)
	}
	if provider.revocations.Load() != 2 {
		t.Fatal("unexpected revoke retries", provider.revocations.Load())
	}
}

func TestPostgresAppleChallengeConsumptionSerializesConcurrentLogins(t *testing.T) {
	svc, repo, provider := appleTestService(t)
	ctx := context.Background()
	data, err := svc.LoginApple(ctx, appleSignupRequest(appleProofFor(t, svc, "login", 0, "")))
	if err != nil {
		t.Fatal(err)
	}
	proof := appleProofFor(t, svc, "login", 0, "")
	challenge, err := repo.AppleChallenge(ctx, proof.ChallengeID, "login", 0, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session := repository.AppleSession{ID: uuid.NewString(), RefreshHash: []byte(uuid.NewString()), ExpiresAt: time.Now().Add(time.Hour), AuthTime: time.Now()}
			_, _, e := repo.CompleteAppleLogin(ctx, challenge, provider.identity, appleidentity.Tokens{RefreshToken: "concurrent-refresh", ClientID: "com.puntazo.test"}, nil, session)
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, repository.ErrAppleChallenge) && !repository.IsRetryable(err) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent successes=%d", success)
	}
	var sessions int
	if err = repo.Pool.QueryRow(ctx, `SELECT count(*) FROM sesiones_auth WHERE usuario_id=$1`, data.User.ID).Scan(&sessions); err != nil || sessions != 2 {
		t.Fatal(sessions, err)
	}
}

func TestPostgresAppleChallengeExpiryAndMerchantSignup(t *testing.T) {
	svc, repo, provider := appleTestService(t)
	ctx := context.Background()
	proof := appleProofFor(t, svc, "login", 0, "")
	if _, err := repo.Pool.Exec(ctx, `UPDATE apple_auth_challenges SET created_at=now()-interval '6 minutes',expires_at=now()-interval '1 minute' WHERE id=$1`, proof.ChallengeID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoginApple(ctx, appleSignupRequest(proof)); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatal("expired nonce accepted", err)
	}
	if provider.exchanges.Load() != 0 {
		t.Fatal("expired nonce exchanged code")
	}
	kind := "PERSONAL_MARCA"
	req := model.AppleAuthRequest{AppleAuthProof: appleProofFor(t, svc, "login", 0, ""), Name: "Merchant", LastName: "Apple", AccountType: &kind, MerchantRegistration: &model.GoogleMerchantRegistration{BrandName: "Apple Brand", BranchName: "Principal", ProgramType: "PUNTOS"}}
	data, err := svc.LoginApple(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !data.AccountCreated || data.User.AccountType != kind {
		t.Fatal(data)
	}
	contexts, err := repo.ListMerchantContexts(ctx, data.User.ID)
	if err != nil || len(contexts) != 1 {
		t.Fatal(contexts, err)
	}
	if contexts[0].Program.Type != "PUNTOS" || contexts[0].Role != "PROPIETARIO" {
		t.Fatal(contexts[0])
	}
}

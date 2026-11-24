package repository_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
)

const browserVerifier = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"

func startBrowserApple(t *testing.T, svc *service.Service, platform, purpose string, userID int64, sessionID string, authVersion int) (model.AppleBrowserStart, string) {
	t.Helper()
	hash := sha256.Sum256([]byte(browserVerifier))
	start, err := svc.StartAppleBrowser(t.Context(), model.AppleBrowserStartRequest{Purpose: purpose, CodeChallenge: base64.RawURLEncoding.EncodeToString(hash[:])}, platform, userID, sessionID, authVersion)
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	if authorize.Host != "appleid.apple.com" || q.Get("state") != start.FlowID || q.Get("scope") != "" ||
		q.Get("response_mode") != "form_post" || q.Get("client_id") != svc.Config.AppleBrowserClientID || q.Get("redirect_uri") != svc.Config.AppleBrowserRedirectURI {
		t.Fatal("invalid authorization URL")
	}
	return start, "test-identity:" + q.Get("nonce")
}

func TestPostgresAppleBrowserPreservesUserAcrossPlatformsAndRevokesBothClients(t *testing.T) {
	svc, repo, native := appleTestService(t)
	browser := &testAppleIdentityProvider{clientID: "com.puntazo.test.browser", identity: native.identity}
	// Subsequent grouped Apple consent can omit email or return a different relay.
	browser.identity.Email = ""
	svc.AppleBrowser = browser
	svc.Config.AppleBrowserClientID = browser.clientID
	svc.Config.AppleBrowserRedirectURI = "https://testing.puntazo.test/api/v1/auth/apple/browser/callback"
	svc.Config.PublicAppURL = "https://testing.puntazo.test"
	svc.Config.AppleBrowserAuthEnabled = true
	ctx := t.Context()

	ios, err := svc.LoginApple(ctx, appleSignupRequest(appleProofFor(t, svc, "login", 0, "")))
	if err != nil {
		t.Fatal(err)
	}
	for platform, target := range map[string]string{"web": "https://testing.puntazo.test/apple-auth-return", "native": "puntazo://apple-auth-return"} {
		start, token := startBrowserApple(t, svc, platform, "login", 0, "", 0)
		finish := model.AppleBrowserFinishRequest{FlowID: start.FlowID, CodeVerifier: browserVerifier}
		if _, err = svc.FinishAppleBrowser(ctx, finish, platform, 0, "", 0); !errors.Is(err, service.ErrAppleAuthPending) {
			t.Fatal("pending canje", err)
		}
		result, err := svc.ReceiveAppleBrowserCallback(ctx, start.FlowID, "test-authorization-code", token, "")
		if err != nil || result != target+"?flow_id="+start.FlowID {
			t.Fatalf("callback %s: %s %v", platform, result, err)
		}
		if _, err = svc.ReceiveAppleBrowserCallback(ctx, start.FlowID, "test-authorization-code", token, ""); !errors.Is(err, service.ErrInvalidCredentials) {
			t.Fatal("repeated callback", err)
		}
		var proof []byte
		if err = repo.Pool.QueryRow(ctx, "SELECT proof FROM apple_browser_flows WHERE id=$1", start.FlowID).Scan(&proof); err != nil || bytes.Contains(proof, []byte(token)) {
			t.Fatal("callback proof stored in plaintext", err)
		}
		wrongPlatform := "web"
		if platform == "web" {
			wrongPlatform = "native"
		}
		if _, err = svc.FinishAppleBrowser(ctx, finish, wrongPlatform, 0, "", 0); !errors.Is(err, service.ErrInvalidCredentials) {
			t.Fatal("different platform accepted", err)
		}
		wrong := finish
		wrong.CodeVerifier = strings.Repeat("x", 50)
		if _, err = svc.FinishAppleBrowser(ctx, wrong, platform, 0, "", 0); !errors.Is(err, service.ErrInvalidCredentials) {
			t.Fatal("different verifier accepted", err)
		}

		var wg sync.WaitGroup
		results := make(chan error, 2)
		before := browser.exchanges.Load()
		for range 2 {
			wg.Go(func() {
				data, err := svc.FinishAppleBrowser(ctx, finish, platform, 0, "", 0)
				if err == nil && (data.User.ID != ios.User.ID || data.AccountCreated || data.Session.RefreshToken == "") {
					err = errors.New("browser login changed identity or returned an incomplete session")
				}
				results <- err
			})
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, service.ErrInvalidCredentials) {
				t.Fatal(err)
			}
		}
		if successes != 1 || browser.exchanges.Load() != before+1 {
			t.Fatal("finish did not consume the Apple code exactly once")
		}
	}
	export, err := repo.ExportAccount(ctx, ios.User.ID)
	if err != nil || export.AppleIdentity == nil || !export.AppleIdentity.PrivateRelay {
		t.Fatal("browser login changed native relay", err)
	}

	_, _, originalSession, authVersion, _, _ := svc.Tokens.ParseSessionContext(ios.Session.AccessToken)
	reauth, token := startBrowserApple(t, svc, "web", "reauthenticate", ios.User.ID, originalSession, authVersion)
	refreshed, err := svc.Refresh(ctx, ios.Session.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	_, _, currentSession, _, _, _ := svc.Tokens.ParseSessionContext(refreshed.Session.AccessToken)
	if currentSession == originalSession {
		t.Fatal("fixture did not exercise family rotation")
	}
	if _, err = svc.ReceiveAppleBrowserCallback(ctx, reauth.FlowID, "test-authorization-code", token, ""); err != nil {
		t.Fatal(err)
	}
	finish := model.AppleBrowserFinishRequest{FlowID: reauth.FlowID, CodeVerifier: browserVerifier, Version: ios.User.Version}
	other, err := svc.LoginApple(ctx, model.AppleAuthRequest{AppleAuthProof: appleProofFor(t, svc, "login", 0, "")})
	if err != nil {
		t.Fatal(err)
	}
	_, _, otherSession, _, _, _ := svc.Tokens.ParseSessionContext(other.Session.AccessToken)
	if _, err = svc.FinishAppleBrowser(ctx, finish, "web", ios.User.ID, otherSession, authVersion); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatal("separate session family accepted", err)
	}
	renewed, err := svc.FinishAppleBrowser(ctx, finish, "web", ios.User.ID, currentSession, authVersion)
	if err != nil || renewed.User.ID != ios.User.ID || renewed.User.AuthVersion != authVersion+1 {
		t.Fatalf("reauth after refresh: user %d %v", renewed.User.ID, err)
	}

	svc.Config.AppleBrowserAuthEnabled = false
	if _, err = svc.AnonymizeCurrentUser(ctx, ios.User.ID, renewed.User.Version, model.AnonymizeAccountRequest{Confirmation: "BAJA"}); err != nil {
		t.Fatal(err)
	}
	if native.revocations.Load() != 1 || browser.revocations.Load() != 1 {
		t.Fatal("deletion did not revoke both Apple clients", native.revocations.Load(), browser.revocations.Load())
	}
}

func TestPostgresAppleBrowserCancellationExpiryAndUnknownSubject(t *testing.T) {
	svc, _, native := appleTestService(t)
	svc.AppleBrowser = &testAppleIdentityProvider{clientID: "com.puntazo.test.browser", identity: native.identity}
	svc.Config.AppleBrowserClientID = "com.puntazo.test.browser"
	svc.Config.AppleBrowserRedirectURI = "https://testing.puntazo.test/api/v1/auth/apple/browser/callback"
	svc.Config.AppleBrowserAuthEnabled = true
	now := time.Now().UTC()
	svc.Now = func() time.Time { return now }
	ctx := t.Context()

	cancelled, _ := startBrowserApple(t, svc, "native", "login", 0, "", 0)
	if _, err := svc.ReceiveAppleBrowserCallback(ctx, cancelled.FlowID, "", "", "user_cancelled_authorize"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAppleBrowser(ctx, model.AppleBrowserFinishRequest{FlowID: cancelled.FlowID, CodeVerifier: browserVerifier}, "native", 0, "", 0); !errors.Is(err, service.ErrAppleAuthCancelled) {
		t.Fatal("wrong cancellation result", err)
	}
	unlinked, token := startBrowserApple(t, svc, "native", "login", 0, "", 0)
	if _, err := svc.ReceiveAppleBrowserCallback(ctx, unlinked.FlowID, "test-authorization-code", token, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinishAppleBrowser(ctx, model.AppleBrowserFinishRequest{FlowID: unlinked.FlowID, CodeVerifier: browserVerifier}, "native", 0, "", 0); !errors.Is(err, repository.ErrAppleLinkRequired) {
		t.Fatal("an unlinked Apple subject was accepted", err)
	}
	expired, token := startBrowserApple(t, svc, "native", "login", 0, "", 0)
	now = now.Add(6 * time.Minute)
	if _, err := svc.ReceiveAppleBrowserCallback(ctx, expired.FlowID, "test-authorization-code", token, ""); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatal("expired flow received a callback", err)
	}
}

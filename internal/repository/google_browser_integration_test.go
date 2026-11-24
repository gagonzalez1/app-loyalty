package repository_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"

	"github.com/gin-gonic/gin"
)

func TestPostgresGoogleBrowserFlowVerifiesAndConsumesOnce(t *testing.T) {
	pool, repo, svc := codeFixture(t)
	ctx := t.Context()
	svc.Config.GoogleBrowserClientID = "web-client.apps.googleusercontent.com"
	svc.Config.GoogleBrowserRedirectURI = svc.Config.PublicAppURL + "/api/v1/auth/google/browser/callback"
	svc.VerifyGoogleBrowserToken = func(_ context.Context, token, audience string, nonceHash []byte) error {
		nonce, ok := strings.CutPrefix(token, "test-token:")
		hash := sha256.Sum256([]byte(nonce))
		if !ok || audience != svc.Config.GoogleBrowserClientID || !bytes.Equal(nonceHash, hash[:]) {
			return auth.ErrInvalidGoogleToken
		}
		return nil
	}

	const verifier = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	startFlow := func() (model.GoogleBrowserStart, string) {
		t.Helper()
		start, err := svc.StartGoogleBrowser(ctx, model.GoogleBrowserStartRequest{CodeChallenge: challenge})
		if err != nil {
			t.Fatal(err)
		}
		authorize, err := url.Parse(start.AuthorizationURL)
		if err != nil {
			t.Fatal(err)
		}
		q := authorize.Query()
		if authorize.Scheme != "https" || authorize.Host != "accounts.google.com" || authorize.Path != "/o/oauth2/v2/auth" ||
			q.Get("client_id") != svc.Config.GoogleBrowserClientID || q.Get("redirect_uri") != svc.Config.GoogleBrowserRedirectURI ||
			q.Get("response_mode") != "form_post" || q.Get("response_type") != "id_token" ||
			q.Get("scope") != "openid email profile" || q.Get("state") != start.FlowID || q.Get("prompt") != "select_account" ||
			len(q.Get("nonce")) != 43 || q.Has("code_challenge") || q.Has("code_verifier") {
			t.Fatal("incorrect provider URL")
		}
		return start, "test-token:" + q.Get("nonce")
	}

	start, token := startFlow()
	finish := model.GoogleBrowserFinishRequest{FlowID: start.FlowID, CodeVerifier: verifier}
	if _, err := svc.FinishGoogleBrowser(ctx, finish); !errors.Is(err, service.ErrGoogleBrowserInvalid) {
		t.Fatalf("pending flow redeemed: %v", err)
	}
	if _, err := svc.ReceiveGoogleBrowserCallback(ctx, start.FlowID, "test-token:wrong-nonce", ""); !errors.Is(err, service.ErrGoogleBrowserInvalid) {
		t.Fatalf("incorrect signed nonce accepted: %v", err)
	}

	h := &handler.Handler{Service: svc, Limiter: middleware.NewRateLimiter()}
	r := gin.New()
	r.POST("/callback", h.GoogleBrowserCallback)
	r.POST("/finish", h.FinishGoogleBrowser)
	post := func(path, mediaType, content string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(content))
		req.Header.Set("Content-Type", mediaType)
		if path == "/finish" {
			req.Header.Set("X-Client-Platform", "web")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	form := url.Values{"state": {start.FlowID}, "id_token": {token}, "iss": {"https://accounts.google.com"}}
	for _, malformed := range []string{
		form.Encode() + "&state=00000000-0000-4000-8000-000000000000",
		form.Encode() + "&error=access_denied",
		form.Encode() + "&extra=" + strings.Repeat("a", 32<<10),
	} {
		if w := post("/callback", "application/x-www-form-urlencoded", malformed); w.Code != http.StatusBadRequest {
			t.Fatalf("malformed callback returned %d", w.Code)
		}
	}
	w := post("/callback?return_to=https://untrusted.example.test", "application/x-www-form-urlencoded", form.Encode())
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != svc.Config.PublicAppURL+"/login?google_flow_id="+start.FlowID ||
		w.Header().Get("Set-Cookie") != "" || w.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(w.Body.String(), token) {
		t.Fatalf("callback response status=%d headers=%v", w.Code, w.Header())
	}
	if w = post("/callback", "application/x-www-form-urlencoded", form.Encode()); w.Code != http.StatusBadRequest {
		t.Fatalf("repeated callback returned %d", w.Code)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT proof FROM google_browser_flows WHERE id=$1", start.FlowID).Scan(&stored); err != nil || len(stored) <= len(token) || bytes.Contains(stored, []byte(token)) {
		t.Fatalf("expected encrypted proof: %v", err)
	}
	if _, err := svc.FinishGoogleBrowser(ctx, model.GoogleBrowserFinishRequest{FlowID: start.FlowID, CodeVerifier: strings.Repeat("x", 43)}); !errors.Is(err, service.ErrGoogleBrowserInvalid) {
		t.Fatalf("a different browser can redeem the flow: %v", err)
	}

	var wg sync.WaitGroup
	out := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Go(func() {
			b, _ := json.Marshal(finish)
			out <- post("/finish", "application/json", string(b))
		})
	}
	wg.Wait()
	close(out)
	successes := 0
	for w := range out {
		switch w.Code {
		case http.StatusOK:
			successes++
			var reply struct {
				Data model.GoogleBrowserFinish `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Data.IDToken != token || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("finish must only return the verified proof")
			}
		case http.StatusBadRequest:
			if !strings.Contains(w.Body.String(), "GOOGLE_BROWSER_INVALID") {
				t.Fatal("missing stable replay error")
			}
		default:
			t.Fatalf("unexpected finish status=%d", w.Code)
		}
	}
	if successes != 1 {
		t.Fatalf("redeemed %d times", successes)
	}
	if err := pool.QueryRow(ctx, "SELECT proof FROM google_browser_flows WHERE id=$1 AND status='used'", start.FlowID).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("consumed proof not cleared: %v", err)
	}

	cancelled, _ := startFlow()
	if target, err := svc.ReceiveGoogleBrowserCallback(ctx, cancelled.FlowID, "", "access_denied"); err != nil || !strings.HasSuffix(target, "/login?google_flow_id="+cancelled.FlowID) {
		t.Fatalf("cancelled callback must return to login: %v", err)
	}
	finish.FlowID = cancelled.FlowID
	if _, err := svc.FinishGoogleBrowser(ctx, finish); !errors.Is(err, service.ErrGoogleBrowserCancelled) {
		t.Fatalf("wrong cancellation error: %v", err)
	}

	expired, expiredToken := startFlow()
	svc.Now = func() time.Time { return expired.ExpiresAt }
	if _, err := svc.ReceiveGoogleBrowserCallback(ctx, expired.FlowID, expiredToken, ""); !errors.Is(err, service.ErrGoogleBrowserInvalid) {
		t.Fatalf("accepted expired callback: %v", err)
	}
	stats, err := repo.ApplyRetention(ctx, repository.RetentionPolicy{Now: expired.ExpiresAt, BatchSize: 10})
	if err != nil || stats.GoogleBrowserFlowsDeleted != 3 {
		t.Fatalf("expired flow retention count=%d err=%v", stats.GoogleBrowserFlowsDeleted, err)
	}
}

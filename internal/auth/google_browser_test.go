package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"google.golang.org/api/idtoken"
)

func TestGoogleBrowserTokenBindsSignedClaimsToFlow(t *testing.T) {
	const audience = "web-client.apps.googleusercontent.com"
	const nonce = "flow-specific-nonce"
	nonceHash := sha256.Sum256([]byte(nonce))
	for _, tc := range []struct {
		name    string
		change  func(*idtoken.Payload)
		failure error
	}{
		{name: "valid web identity"},
		{name: "legacy Google issuer", change: func(p *idtoken.Payload) { p.Issuer = "accounts.google.com" }},
		{name: "different nonce", change: func(p *idtoken.Payload) { p.Claims["nonce"] = "another-flow" }, failure: ErrInvalidGoogleToken},
		{name: "missing nonce", change: func(p *idtoken.Payload) { delete(p.Claims, "nonce") }, failure: ErrInvalidGoogleToken},
		{name: "different issuer", change: func(p *idtoken.Payload) { p.Issuer = "https://issuer.invalid" }, failure: ErrInvalidGoogleToken},
		{name: "Android audience", change: func(p *idtoken.Payload) { p.Audience = "android.apps.googleusercontent.com" }, failure: ErrInvalidGoogleToken},
		{name: "different presenter", change: func(p *idtoken.Payload) { p.Claims["azp"] = "another-client" }, failure: ErrInvalidGoogleToken},
		{name: "unverified email", change: func(p *idtoken.Payload) { p.Claims["email_verified"] = false }, failure: ErrInvalidGoogleToken},
		{name: "missing subject", change: func(p *idtoken.Payload) { p.Subject = "" }, failure: ErrInvalidGoogleToken},
		{name: "signature rejected", failure: errors.New("validator rejects signature")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := &idtoken.Payload{
				Audience: audience, Issuer: "https://accounts.google.com", Subject: "google-subject",
				Claims: map[string]any{"email": "fixture@example.test", "email_verified": true, "nonce": nonce},
			}
			if tc.change != nil {
				tc.change(payload)
			}
			err := verifyGoogleBrowserToken(t.Context(), "test-google-token", audience, nonceHash[:],
				func(_ context.Context, token, validatedAudience string) (*idtoken.Payload, error) {
					if token != "test-google-token" || validatedAudience != audience {
						t.Fatal("validator received the wrong token or audience")
					}
					if tc.failure != nil && !errors.Is(tc.failure, ErrInvalidGoogleToken) {
						return nil, tc.failure
					}
					return payload, nil
				})
			if (tc.failure == nil && err != nil) || (tc.failure != nil && !errors.Is(err, ErrInvalidGoogleToken)) {
				t.Fatalf("validation error = %v, expected invalid=%v", err, tc.failure != nil)
			}
		})
	}
}

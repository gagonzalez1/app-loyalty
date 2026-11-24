package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"

	"google.golang.org/api/idtoken"
)

// VerifyGoogleBrowserToken uses only the web audience and binds Google's signed
// nonce to the browser flow. The generic Google login also accepts Android.
func VerifyGoogleBrowserToken(ctx context.Context, idToken, audience string, nonceHash []byte) error {
	return verifyGoogleBrowserToken(ctx, idToken, audience, nonceHash, idtoken.Validate)
}

func verifyGoogleBrowserToken(ctx context.Context, idToken, audience string, nonceHash []byte, validate func(context.Context, string, string) (*idtoken.Payload, error)) error {
	payload, err := validate(ctx, idToken, audience)
	if err != nil || payload == nil || audience == "" || payload.Audience != audience || payload.Subject == "" ||
		(payload.Issuer != "https://accounts.google.com" && payload.Issuer != "accounts.google.com") {
		return ErrInvalidGoogleToken
	}
	// When present, azp names the authorized presenter of the token.
	if azp, ok := payload.Claims["azp"]; ok && azp != audience {
		return ErrInvalidGoogleToken
	}
	verified, _ := payload.Claims["email_verified"].(bool)
	email, _ := payload.Claims["email"].(string)
	nonce, _ := payload.Claims["nonce"].(string)
	hash := sha256.Sum256([]byte(nonce))
	if !verified || email == "" || nonce == "" || subtle.ConstantTimeCompare(hash[:], nonceHash) != 1 {
		return ErrInvalidGoogleToken
	}
	return nil
}

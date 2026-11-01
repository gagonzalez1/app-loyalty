package appleidentity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testAppleClient(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (*Client, *rsa.PrivateKey, *ecdsa.PrivateKey, *atomic.Int32) {
	t.Helper()
	signing, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	hits := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/keys" {
			hits.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kid": "apple-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(signing.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(signing.E)).Bytes())}}})
			return
		}
		if handler == nil {
			http.Error(w, "not available", 503)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{ClientID: "com.puntazo.test", TeamID: "TEAMID", KeyID: "CLIENTKEY", PrivateKeyPEM: string(keyPEM), HTTPClient: server.Client(), JWKSURL: server.URL + "/keys", TokenURL: server.URL + "/token", RevokeURL: server.URL + "/revoke"})
	if err != nil {
		t.Fatal(err)
	}
	return client, signing, clientKey, hits
}

func signApple(t *testing.T, key *rsa.PrivateKey, changes map[string]any) string {
	t.Helper()
	claims := jwt.MapClaims{"iss": issuer, "aud": "com.puntazo.test", "sub": "apple-subject", "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Minute).Unix(), "nonce": "server-nonce", "email": "private@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"}
	for k, v := range changes {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "apple-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAppleIdentityRejectsSubstitutedClaimsAndCachesJWKS(t *testing.T) {
	c, key, _, hits := testAppleClient(t, nil)
	hash := sha256.Sum256([]byte("server-nonce"))
	identity, err := c.Verify(context.Background(), signApple(t, key, nil), hash[:])
	if err != nil || identity.Subject != "apple-subject" || !identity.PrivateRelay || !identity.EmailVerified {
		t.Fatal(identity, err)
	}
	for name, changes := range map[string]map[string]any{
		"audience": {"aud": "another-app"}, "issuer": {"iss": "https://attacker.test"}, "expired": {"exp": time.Now().Add(-time.Minute).Unix()}, "future-issued": {"iat": time.Now().Add(time.Hour).Unix()}, "no-expiry": {"exp": nil}, "no-subject": {"sub": nil}, "wrong-nonce": {"nonce": "different"}, "no-nonce": {"nonce": nil}, "unverified-email": {"email_verified": false}, "invalid-bool": {"email_verified": "yes"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Verify(context.Background(), signApple(t, key, changes), hash[:]); !errors.Is(err, ErrInvalidToken) {
				t.Fatal(err)
			}
		})
	}
	if hits.Load() != 1 {
		t.Fatalf("valid key cache not used: requests=%d", hits.Load())
	}
	for _, raw := range []string{"", strings.Repeat("x", 16385), "not.a.jwt"} {
		if _, err := c.Verify(context.Background(), raw, hash[:]); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(err)
		}
	}
	wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := c.Verify(context.Background(), signApple(t, wrongKey, nil), hash[:]); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("signature accepted", err)
	}
}

func TestAppleCodeExchangeBindsNonceSubjectAndClientSecret(t *testing.T) {
	var signing *rsa.PrivateKey
	var clientSigning *ecdsa.PrivateKey
	var wrongSubject bool
	var revoked atomic.Int32
	c, key, clientKey, _ := testAppleClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		claims := new(jwt.RegisteredClaims)
		_, err := jwt.ParseWithClaims(r.Form.Get("client_secret"), claims, func(token *jwt.Token) (any, error) {
			if token.Header["kid"] != "CLIENTKEY" {
				t.Error("wrong key id")
			}
			return &clientSigning.PublicKey, nil
		}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience(issuer), jwt.WithIssuer("TEAMID"), jwt.WithSubject("com.puntazo.test"), jwt.WithExpirationRequired())
		if err != nil {
			t.Error("invalid client secret", err)
			http.Error(w, "invalid", 400)
			return
		}
		if r.Form.Get("client_id") != "com.puntazo.test" {
			t.Error("wrong client")
		}
		if r.URL.Path == "/revoke" {
			if r.Form.Get("token") != "refresh-secret" || r.Form.Get("token_type_hint") != "refresh_token" {
				t.Error("wrong revoke input")
			}
			revoked.Add(1)
			w.WriteHeader(200)
			return
		}
		if r.Form.Get("code") != "single-use-code" || r.Form.Get("grant_type") != "authorization_code" {
			t.Error("wrong grant")
		}
		changes := map[string]any{}
		if wrongSubject {
			changes["sub"] = "another-subject"
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "access-secret", "refresh_token": "refresh-secret", "id_token": signApple(t, signing, changes)})
	})
	signing = key
	clientSigning = clientKey
	hash := sha256.Sum256([]byte("server-nonce"))
	identity := Identity{Subject: "apple-subject"}
	tokens, err := c.Exchange(context.Background(), "single-use-code", identity, hash[:])
	if err != nil || tokens.RefreshToken != "refresh-secret" {
		t.Fatal(tokens, err)
	}
	if err = c.Revoke(context.Background(), tokens.RefreshToken, "refresh_token"); err != nil || revoked.Load() != 1 {
		t.Fatal(err)
	}
	wrongSubject = true
	if _, err = c.Exchange(context.Background(), "single-use-code", identity, hash[:]); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("exchanged different identity", err)
	}
}

func TestAppleClientConfigurationAndCancellation(t *testing.T) {
	c, err := New(Config{})
	if c != nil || err != nil {
		t.Fatal("missing configuration must be explicitly disabled")
	}
	if _, err = New(Config{ClientID: "only-partial"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	c, key, _, _ := testAppleClient(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hash := sha256.Sum256([]byte("server-nonce"))
	if _, err = c.Verify(ctx, signApple(t, key, nil), hash[:]); err == nil {
		t.Fatal("canceled JWKS request accepted")
	}
}

func TestAppleTokenEncryptionBindsPurposeAndRejectsTampering(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	encrypted, nonce, err := EncryptToken(key, "client-id", "refresh_token", "secret-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), "secret-refresh") {
		t.Fatal("plaintext persisted")
	}
	token, err := DecryptToken(key, "client-id", "refresh_token", encrypted, nonce)
	if err != nil || token != "secret-refresh" {
		t.Fatal(token, err)
	}
	for _, pair := range [][2]string{{"wrong-client", "refresh_token"}, {"client-id", "access_token"}} {
		if _, err = DecryptToken(key, pair[0], pair[1], encrypted, nonce); err == nil {
			t.Fatal("swapped AAD accepted")
		}
	}
	encrypted[0] ^= 0x80
	if _, err = DecryptToken(key, "client-id", "refresh_token", encrypted, nonce); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, _, err = EncryptToken([]byte("short"), "client-id", "refresh_token", "secret"); !errors.Is(err, ErrCipher) {
		t.Fatal(err)
	}
}

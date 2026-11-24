// Package appleidentity implements Sign in with Apple without a test-identity
// fallback. Request cancellation and HTTP timeouts bound every provider call.
package appleidentity

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrUnavailable  = errors.New("apple identity unavailable")
	ErrInvalidToken = errors.New("invalid apple identity")
	ErrProvider     = errors.New("apple identity provider failure")
)

const issuer = "https://appleid.apple.com"

type Config struct {
	ClientID, TeamID, KeyID, PrivateKeyPEM, RedirectURI string
	// Overrides are for explicit test servers; operational configuration uses
	// Apple's endpoints and must not derive URLs from requests or JWT headers.
	HTTPClient                   *http.Client
	JWKSURL, TokenURL, RevokeURL string
	Now                          func() time.Time
}

type Identity struct {
	Subject, Email              string
	EmailVerified, PrivateRelay bool
}

type Tokens struct {
	AccessToken, RefreshToken, ClientID string
}

func (t Tokens) Revocable() (token, tokenType string) {
	if t.RefreshToken != "" {
		return t.RefreshToken, "refresh_token"
	}
	return t.AccessToken, "access_token"
}

type Client struct {
	config    Config
	key       *ecdsa.PrivateKey
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	keysUntil time.Time
}

// New returns nil when all credentials are absent. Partial configuration fails
// closed rather than silently accepting identities without code exchange.
func New(cfg Config) (*Client, error) {
	if cfg.ClientID == "" && cfg.TeamID == "" && cfg.KeyID == "" && cfg.PrivateKeyPEM == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.TeamID) == "" || strings.TrimSpace(cfg.KeyID) == "" || cfg.PrivateKeyPEM == "" {
		return nil, ErrUnavailable
	}
	block, _ := pem.Decode([]byte(cfg.PrivateKeyPEM))
	if block == nil {
		return nil, ErrUnavailable
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrUnavailable
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().BitSize != 256 {
		return nil, ErrUnavailable
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 8 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = issuer + "/auth/keys"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = issuer + "/auth/token"
	}
	if cfg.RevokeURL == "" {
		cfg.RevokeURL = issuer + "/auth/revoke"
	}
	return &Client{config: cfg, key: key}, nil
}

func (c *Client) Enabled() bool { return c != nil && c.key != nil }

type appleClaims struct {
	jwt.RegisteredClaims
	Nonce         string       `json:"nonce"`
	Email         string       `json:"email"`
	EmailVerified flexibleBool `json:"email_verified"`
	PrivateEmail  flexibleBool `json:"is_private_email"`
}

type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(raw []byte) error {
	if string(raw) == "true" || string(raw) == `"true"` {
		*b = true
		return nil
	}
	if string(raw) == "false" || string(raw) == `"false"` {
		*b = false
		return nil
	}
	return ErrInvalidToken
}

func (c *Client) Verify(ctx context.Context, raw string, nonceHash []byte) (Identity, error) {
	if !c.Enabled() {
		return Identity{}, ErrUnavailable
	}
	if len(raw) == 0 || len(raw) > 16384 || len(nonceHash) != sha256.Size {
		return Identity{}, ErrInvalidToken
	}
	claims := new(appleClaims)
	_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" || len(kid) > 256 || token.Method.Alg() != "RS256" {
			return nil, ErrInvalidToken
		}
		return c.publicKey(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(c.config.ClientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(c.config.Now))
	if err != nil {
		if errors.Is(err, ErrProvider) {
			return Identity{}, ErrProvider
		}
		return Identity{}, ErrInvalidToken
	}
	nonce := sha256.Sum256([]byte(claims.Nonce))
	if claims.Subject == "" || len(claims.Subject) > 255 || claims.Nonce == "" || subtle.ConstantTimeCompare(nonce[:], nonceHash) != 1 {
		return Identity{}, ErrInvalidToken
	}
	if claims.Email != "" && !bool(claims.EmailVerified) {
		return Identity{}, ErrInvalidToken
	}
	return Identity{Subject: claims.Subject, Email: claims.Email, EmailVerified: bool(claims.EmailVerified), PrivateRelay: bool(claims.PrivateEmail)}, nil
}

func (c *Client) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.Now().Before(c.keysUntil) && c.keys[kid] != nil {
		return c.keys[kid], nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.JWKSURL, nil)
	if err != nil {
		return nil, ErrProvider
	}
	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, ErrProvider
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrProvider
	}
	var body struct {
		Keys []struct {
			KID string `json:"kid"`
			KTY string `json:"kty"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return nil, ErrProvider
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, k := range body.Keys {
		if k.KTY != "RSA" || k.Alg != "RS256" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, e1 := base64.RawURLEncoding.DecodeString(k.N)
		e, e2 := base64.RawURLEncoding.DecodeString(k.E)
		if e1 != nil || e2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := 0
		for _, v := range e {
			exponent = exponent*256 + int(v)
		}
		modulus := new(big.Int).SetBytes(n)
		if modulus.BitLen() < 2048 || exponent < 3 || exponent%2 == 0 {
			continue
		}
		keys[k.KID] = &rsa.PublicKey{N: modulus, E: exponent}
	}
	c.keys = keys
	c.keysUntil = c.config.Now().Add(time.Hour)
	if keys[kid] == nil {
		return nil, ErrInvalidToken
	}
	return keys[kid], nil
}

func (c *Client) clientSecret() (string, error) {
	now := c.config.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{Issuer: c.config.TeamID, Subject: c.config.ClientID, Audience: jwt.ClaimStrings{issuer}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute))})
	token.Header["kid"] = c.config.KeyID
	return token.SignedString(crypto.Signer(c.key))
}

func (c *Client) Exchange(ctx context.Context, code string, identity Identity, nonceHash []byte) (Tokens, error) {
	if !c.Enabled() {
		return Tokens{}, ErrUnavailable
	}
	if code == "" || len(code) > 4096 {
		return Tokens{}, ErrInvalidToken
	}
	secret, err := c.clientSecret()
	if err != nil {
		return Tokens{}, ErrProvider
	}
	form := url.Values{"client_id": {c.config.ClientID}, "client_secret": {secret}, "code": {code}, "grant_type": {"authorization_code"}}
	if c.config.RedirectURI != "" {
		form.Set("redirect_uri", c.config.RedirectURI)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err = c.post(ctx, c.config.TokenURL, form, &body); err != nil {
		return Tokens{}, err
	}
	verified, err := c.Verify(ctx, body.IDToken, nonceHash)
	if err != nil || verified.Subject != identity.Subject || (body.AccessToken == "" && body.RefreshToken == "") {
		return Tokens{}, ErrInvalidToken
	}
	return Tokens{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, ClientID: c.config.ClientID}, nil
}

func (c *Client) Revoke(ctx context.Context, token, tokenType string) error {
	if !c.Enabled() {
		return ErrUnavailable
	}
	if token == "" || (tokenType != "refresh_token" && tokenType != "access_token") {
		return ErrInvalidToken
	}
	secret, err := c.clientSecret()
	if err != nil {
		return ErrProvider
	}
	return c.post(ctx, c.config.RevokeURL, url.Values{"client_id": {c.config.ClientID}, "client_secret": {secret}, "token": {token}, "token_type_hint": {tokenType}}, nil)
}

func (c *Client) post(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrProvider
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return ErrProvider
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 400 {
			return ErrInvalidToken
		}
		return ErrProvider
	}
	if out != nil && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return ErrProvider
	}
	return nil
}

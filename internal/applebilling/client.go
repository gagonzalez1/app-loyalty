// Package applebilling validates StoreKit signed data and calls Apple's server API.
// Trust roots are explicit; local StoreKit certificates are accepted only in Xcode mode.
package applebilling

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"clientesFrecuentes/internal/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/ocsp"
)

var ErrInvalidSignedData = errors.New("invalid Apple signed data")
var ErrUnavailable = errors.New("Apple billing unavailable")

type Config struct {
	PurchasesEnabled                     bool
	Enabled                              bool
	Environment                          string
	BundleID                             string
	AppAppleID                           int64
	IssuerID, KeyID, SubscriptionGroupID string
	PrivateKeyPEM, RootsPEM              []byte
	AllowXcode, Production               bool
}
type Client struct {
	cfg         Config
	roots       *x509.CertPool
	key         *ecdsa.PrivateKey
	http        *http.Client
	now         func() time.Time
	base        string
	ocspMu      sync.Mutex
	ocspCache   map[string]time.Time
	ocspFlights map[string]*ocspFlight
}
type ocspFlight struct {
	done    chan struct{}
	err     error
	expires time.Time
}

func New(cfg Config, client *http.Client) (*Client, error) {
	if cfg.Environment == "" {
		cfg.Environment = "Production"
	}
	if cfg.Environment != "Production" && cfg.Environment != "Sandbox" && cfg.Environment != "Xcode" {
		return nil, errors.New("invalid Apple billing environment")
	}
	if cfg.Production && (cfg.Environment != "Production" || cfg.AllowXcode) {
		return nil, errors.New("production rejects Apple test environments")
	}
	if cfg.Environment == "Xcode" && (!cfg.AllowXcode || cfg.Production) {
		return nil, errors.New("Xcode requires explicit local configuration")
	}
	c := &Client{cfg: cfg, http: client, now: time.Now, base: "https://api.storekit.apple.com"}
	if c.http == nil {
		c.http = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Environment == "Sandbox" {
		c.base = "https://api.storekit-sandbox.apple.com"
	}
	if cfg.PurchasesEnabled && !cfg.Enabled {
		return nil, errors.New("purchases require enabled Apple verifier")
	}
	if !cfg.Enabled {
		return c, nil
	}
	if cfg.BundleID == "" || len(cfg.RootsPEM) == 0 {
		return nil, errors.New("Apple bundle and trusted root certificates required")
	}
	c.roots = x509.NewCertPool()
	for data := cfg.RootsPEM; len(data) > 0; {
		block, rest := pem.Decode(data)
		if block == nil {
			return nil, errors.New("invalid Apple root PEM")
		}
		data = rest
		cert, e := x509.ParseCertificate(block.Bytes)
		if e != nil || (!cert.IsCA && cfg.Environment != "Xcode") || cert.CheckSignatureFrom(cert) != nil {
			return nil, errors.New("invalid trusted root")
		}
		c.roots.AddCert(cert)
	}
	if cfg.Environment != "Xcode" {
		if cfg.IssuerID == "" || cfg.KeyID == "" || cfg.AppAppleID < 1 || cfg.SubscriptionGroupID == "" {
			return nil, errors.New("Apple issuer, key, app Apple ID and subscription group ID required")
		}
		key, e := jwt.ParseECPrivateKeyFromPEM(cfg.PrivateKeyPEM)
		if e != nil {
			return nil, errors.New("invalid Apple server key")
		}
		c.key = key
	}
	return c, nil
}
func (c *Client) Enabled() bool          { return c != nil && c.cfg.Enabled }
func (c *Client) PurchasesEnabled() bool { return c.Enabled() && c.cfg.PurchasesEnabled }
func (c *Client) Environment() string    { return c.cfg.Environment }

func Catalog() []model.AppleProduct {
	products := make([]model.AppleProduct, 0, 20)
	for _, kind := range []string{"SELLOS", "PUNTOS"} {
		for capacity := 1; capacity <= 10; capacity++ {
			products = append(products, model.AppleProduct{ProductID: fmt.Sprintf("com.puntazo.app.merchant.%s.%d.monthly", strings.ToLower(kind), capacity), ProgramType: kind, Capacity: capacity, Period: "P1M"})
		}
	}
	return products
}
func Product(id string) (model.AppleProduct, bool) {
	for _, p := range Catalog() {
		if p.ProductID == id {
			return p, true
		}
	}
	return model.AppleProduct{}, false
}

func (c *Client) verify(ctx context.Context, signed string, out any) error {
	if !c.Enabled() {
		return ErrUnavailable
	}
	if len(signed) > 512*1024 {
		return ErrInvalidSignedData
	}
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		return ErrInvalidSignedData
	}
	headerBytes, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return ErrInvalidSignedData
	}
	var header struct {
		Alg string   `json:"alg"`
		X5C []string `json:"x5c"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Alg != "ES256" || (len(header.X5C) < 1 || len(header.X5C) > 3 || (c.cfg.Environment != "Xcode" && len(header.X5C) != 3)) {
		return ErrInvalidSignedData
	}
	certificates := make([]*x509.Certificate, 0, 3)
	for _, encoded := range header.X5C {
		der, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			return ErrInvalidSignedData
		}
		cert, e := x509.ParseCertificate(der)
		if e != nil {
			return ErrInvalidSignedData
		}
		certificates = append(certificates, cert)
	}
	payload, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return ErrInvalidSignedData
	}
	var dates struct {
		SignedDate int64 `json:"signedDate"`
	}
	if json.Unmarshal(payload, &dates) != nil || dates.SignedDate <= 0 || time.UnixMilli(dates.SignedDate).After(c.now().Add(5*time.Minute)) {
		return ErrInvalidSignedData
	}
	intermediates := x509.NewCertPool()
	if len(certificates) > 1 {
		intermediates.AddCert(certificates[1])
	}
	verificationTime := c.now()
	if c.cfg.Environment == "Xcode" {
		verificationTime = time.UnixMilli(dates.SignedDate)
	}
	chains, verifyErr := certificates[0].Verify(x509.VerifyOptions{Roots: c.roots, Intermediates: intermediates, CurrentTime: verificationTime, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if verifyErr != nil {
		return ErrInvalidSignedData
	}
	// OCSP issuer certificates must be the issuers in the trusted chain, not
	// merely arbitrary additional certificates supplied in the JWS header.
	exactChain := false
	for _, chain := range chains {
		if len(chain) != len(certificates) {
			continue
		}
		same := true
		for i := range chain {
			if !bytes.Equal(chain[i].Raw, certificates[i].Raw) {
				same = false
				break
			}
		}
		if same {
			exactChain = true
			break
		}
	}
	if !exactChain {
		return ErrInvalidSignedData
	}
	for i := 0; i+1 < len(certificates); i++ {
		if certificates[i].CheckSignatureFrom(certificates[i+1]) != nil {
			return ErrInvalidSignedData
		}
	}

	// Reject forged JWS before making any revocation-network requests. The
	// public x5c chain alone is not evidence that the sender owns its leaf key.
	parsed, e := jwt.Parse(signed, func(token *jwt.Token) (any, error) { return certificates[0].PublicKey, nil }, jwt.WithValidMethods([]string{"ES256"}), jwt.WithoutClaimsValidation())
	if e != nil || !parsed.Valid {
		return ErrInvalidSignedData
	}
	if c.cfg.Environment != "Xcode" {
		if !hasExtension(certificates[0], asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}) || !hasExtension(certificates[1], asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}) {
			return ErrInvalidSignedData
		}
		if e = c.checkOCSP(ctx, certificates[0], certificates[1]); e != nil {
			return e
		}
		if e = c.checkOCSP(ctx, certificates[1], certificates[2]); e != nil {
			return e
		}
	}
	if json.Unmarshal(payload, out) != nil {
		return ErrInvalidSignedData
	}
	return nil
}
func hasExtension(cert *x509.Certificate, id asn1.ObjectIdentifier) bool {
	for _, e := range cert.Extensions {
		if e.Id.Equal(id) {
			return true
		}
	}
	return false
}

// checkOCSP coalesces concurrent requests for one certificate/issuer and caches
// only validated Good responses. No stale-while-revalidate or negative cache:
// expiry/provider failure fails closed, and cancellation reaches the leader's HTTP request.
func (c *Client) checkOCSP(ctx context.Context, cert, issuer *x509.Certificate) error {
	digest := sha256.New()
	_, _ = digest.Write(cert.Raw)
	_, _ = digest.Write(issuer.Raw)
	key := string(digest.Sum(nil))
	c.ocspMu.Lock()
	if expiry, ok := c.ocspCache[key]; ok && c.now().Before(expiry) {
		c.ocspMu.Unlock()
		return nil
	}
	if flight, ok := c.ocspFlights[key]; ok {
		c.ocspMu.Unlock()
		select {
		case <-ctx.Done():
			return ErrUnavailable
		case <-flight.done:
			if flight.err == nil && !c.now().Before(flight.expires) {
				return ErrInvalidSignedData
			}
			return flight.err
		}
	}
	if c.ocspFlights == nil {
		c.ocspFlights = map[string]*ocspFlight{}
	}
	flight := &ocspFlight{done: make(chan struct{})}
	c.ocspFlights[key] = flight
	c.ocspMu.Unlock()
	expiry, err := c.fetchOCSP(ctx, cert, issuer)
	c.ocspMu.Lock()
	if err == nil && !c.now().Before(expiry) {
		err = ErrInvalidSignedData
	}
	if err == nil {
		// Refresh at least every five minutes even when the responder advertises
		// a longer lifetime; never keep a response beyond its signed NextUpdate.
		maxExpiry := c.now().Add(5 * time.Minute)
		if expiry.After(maxExpiry) {
			expiry = maxExpiry
		}
		if c.ocspCache == nil {
			c.ocspCache = map[string]time.Time{}
		}
		c.ocspCache[key] = expiry
		flight.expires = expiry
	}
	flight.err = err
	delete(c.ocspFlights, key)
	close(flight.done)
	c.ocspMu.Unlock()
	return err
}
func (c *Client) fetchOCSP(ctx context.Context, cert, issuer *x509.Certificate) (time.Time, error) {
	if len(cert.OCSPServer) == 0 {
		return time.Time{}, ErrInvalidSignedData
	}
	body, e := ocsp.CreateRequest(cert, issuer, nil)
	if e != nil {
		return time.Time{}, ErrInvalidSignedData
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, cert.OCSPServer[0], bytes.NewReader(body))
	if e != nil {
		return time.Time{}, ErrInvalidSignedData
	}
	req.Header.Set("Content-Type", "application/ocsp-request")
	resp, e := c.http.Do(req)
	if e != nil {
		return time.Time{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return time.Time{}, ErrUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if e != nil {
		return time.Time{}, ErrUnavailable
	}
	status, e := ocsp.ParseResponseForCert(data, cert, issuer)
	if e != nil || status.Status != ocsp.Good || status.ThisUpdate.IsZero() || status.ThisUpdate.After(c.now().Add(5*time.Minute)) || status.NextUpdate.IsZero() || status.NextUpdate.Before(c.now()) {
		return time.Time{}, ErrInvalidSignedData
	}
	return status.NextUpdate, nil
}
func (c *Client) VerifyTransaction(ctx context.Context, signed string) (model.AppleTransaction, error) {
	return c.verifyTransaction(ctx, signed, true)
}
func (c *Client) verifyTransaction(ctx context.Context, signed string, requireKnownProduct bool) (model.AppleTransaction, error) {
	var t model.AppleTransaction
	if e := c.verify(ctx, signed, &t); e != nil {
		return t, e
	}
	if t.BundleID != c.cfg.BundleID || t.Environment != c.cfg.Environment || t.TransactionID == "" || t.OriginalTransactionID == "" || t.Type != "Auto-Renewable Subscription" || t.OwnershipType != "PURCHASED" || t.PurchaseDate <= 0 || t.ExpiresDate <= t.PurchaseDate || t.Price < 0 {
		return t, ErrInvalidSignedData
	}
	if c.cfg.SubscriptionGroupID != "" && t.SubscriptionGroupID != c.cfg.SubscriptionGroupID {
		return t, ErrInvalidSignedData
	}
	if _, ok := Product(t.ProductID); requireKnownProduct && !ok {
		return t, ErrInvalidSignedData
	}
	if t.AppAccountToken != "" {
		if _, e := uuid.Parse(t.AppAccountToken); e != nil {
			return t, ErrInvalidSignedData
		}
	}
	if t.OfferType == 1 {
		return t, ErrInvalidSignedData
	}
	if t.Price > 0 && (len(t.Currency) != 3 || strings.ToUpper(t.Currency) != t.Currency) {
		return t, ErrInvalidSignedData
	}
	return t, nil
}
func (c *Client) verifyRenewal(ctx context.Context, signed string) (model.AppleRenewal, error) {
	var r model.AppleRenewal
	e := c.verify(ctx, signed, &r)
	if e != nil {
		return r, e
	}
	if r.Environment != c.cfg.Environment || r.OriginalTransactionID == "" || (r.AutoRenewStatus != 0 && r.AutoRenewStatus != 1) {
		return r, ErrInvalidSignedData
	}
	return r, nil
}
func (c *Client) VerifyNotification(ctx context.Context, signed string) (model.AppleNotification, error) {
	var n model.AppleNotification
	if e := c.verify(ctx, signed, &n); e != nil {
		return n, e
	}
	if _, e := uuid.Parse(n.NotificationUUID); e != nil || n.Version != "2.0" || n.Data.Environment != c.cfg.Environment || n.Data.BundleID != c.cfg.BundleID || (c.cfg.Environment == "Production" && n.Data.AppAppleID != c.cfg.AppAppleID) {
		return n, ErrInvalidSignedData
	}
	if n.Data.SignedTransaction != "" {
		t, e := c.verifyTransaction(ctx, n.Data.SignedTransaction, false)
		if e != nil {
			return n, e
		}
		if n.NotificationType == "REFUND_REVERSED" && t.RevocationDate == 0 {
			t.RevocationReversed = true
		}
		n.Transaction = &t
	}
	if n.Data.SignedRenewal != "" {
		r, e := c.verifyRenewal(ctx, n.Data.SignedRenewal)
		if e != nil {
			return n, e
		}
		if n.Transaction != nil && r.OriginalTransactionID != n.Transaction.OriginalTransactionID {
			return n, ErrInvalidSignedData
		}
		n.Renewal = &r
	}
	return n, nil
}
func (c *Client) api(ctx context.Context, method, path string, body any, out any) error {
	if !c.Enabled() || c.key == nil {
		return ErrUnavailable
	}
	claims := jwt.MapClaims{"iss": c.cfg.IssuerID, "iat": c.now().Unix(), "exp": c.now().Add(5 * time.Minute).Unix(), "aud": "appstoreconnect-v1", "bid": c.cfg.BundleID}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = c.cfg.KeyID
	token.Header["typ"] = "JWT"
	bearer, e := token.SignedString(c.key)
	if e != nil {
		return ErrUnavailable
	}
	var encoded []byte
	if body != nil {
		encoded, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(encoded))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrUnavailable
	}
	if out == nil {
		return nil
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out) != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) SubscriptionStatuses(ctx context.Context, id string) ([]model.AppleSubscriptionStatus, error) {
	var response struct {
		Environment string `json:"environment"`
		BundleID    string `json:"bundleId"`
		Data        []struct {
			LastTransactions []struct {
				Status            int    `json:"status"`
				SignedTransaction string `json:"signedTransactionInfo"`
				SignedRenewal     string `json:"signedRenewalInfo"`
			} `json:"lastTransactions"`
		} `json:"data"`
	}
	if e := c.api(ctx, http.MethodGet, "/inApps/v1/subscriptions/"+url.PathEscape(id), nil, &response); e != nil {
		return nil, e
	}
	if response.Environment != c.cfg.Environment || response.BundleID != c.cfg.BundleID {
		return nil, ErrInvalidSignedData
	}
	result := []model.AppleSubscriptionStatus{}
	for _, group := range response.Data {
		for _, item := range group.LastTransactions {
			t, e := c.VerifyTransaction(ctx, item.SignedTransaction)
			if e != nil {
				return nil, e
			}
			r, e := c.verifyRenewal(ctx, item.SignedRenewal)
			if e != nil || r.OriginalTransactionID != t.OriginalTransactionID {
				return nil, ErrInvalidSignedData
			}
			if (item.Status == 1 || item.Status == 4) && t.RevocationDate == 0 {
				t.RevocationReversed = true
			}
			if t.OriginalTransactionID == id {
				result = append(result, model.AppleSubscriptionStatus{Status: item.Status, Transaction: t, Renewal: r})
			}
		}
	}
	if len(result) == 0 {
		return nil, ErrUnavailable
	}
	return result, nil
}
func (c *Client) SetAppAccountToken(ctx context.Context, id, token string) error {
	if _, e := uuid.Parse(token); e != nil {
		return ErrInvalidSignedData
	}
	return c.api(ctx, http.MethodPut, "/inApps/v1/transactions/"+url.PathEscape(id)+"/appAccountToken", map[string]string{"appAccountToken": token}, nil)
}

// Fingerprint excludes signed payloads, which must never enter logs or idempotency errors.
func Fingerprint(product, referral string) []byte {
	sum := sha256.Sum256([]byte(product + "\x00" + strings.ToUpper(strings.TrimSpace(referral))))
	return sum[:]
}

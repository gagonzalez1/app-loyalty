package applebilling

import (
	"clientesFrecuentes/internal/model"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/ocsp"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type signedFixture struct {
	issuerKey *ecdsa.PrivateKey
	client    *Client
	key       *ecdsa.PrivateKey
	chain     []string
	now       time.Time
}

func certificateFixture(t *testing.T) signedFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	keys := []*ecdsa.PrivateKey{}
	for i := 0; i < 3; i++ {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		keys = append(keys, k)
	}
	templates := []*x509.Certificate{}
	for i := 0; i < 3; i++ {
		templates = append(templates, &x509.Certificate{SerialNumber: big.NewInt(int64(i + 1)), Subject: pkix.Name{CommonName: []string{"local StoreKit leaf", "local intermediate", "local root"}[i]}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, IsCA: i > 0, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign})
	}
	templates[0].OCSPServer = []string{"https://ocsp.apple.test/leaf"}
	templates[1].OCSPServer = []string{"https://ocsp.apple.test/intermediate"}
	templates[0].ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}, Value: []byte{5, 0}}}
	templates[1].ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}, Value: []byte{5, 0}}}
	der := make([][]byte, 3)
	var e error
	der[2], e = x509.CreateCertificate(rand.Reader, templates[2], templates[2], &keys[2].PublicKey, keys[2])
	if e != nil {
		t.Fatal(e)
	}
	der[1], e = x509.CreateCertificate(rand.Reader, templates[1], templates[2], &keys[1].PublicKey, keys[2])
	if e != nil {
		t.Fatal(e)
	}
	der[0], e = x509.CreateCertificate(rand.Reader, templates[0], templates[1], &keys[0].PublicKey, keys[1])
	if e != nil {
		t.Fatal(e)
	}
	roots := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der[2]})
	c, e := New(Config{Enabled: true, Environment: "Xcode", AllowXcode: true, BundleID: "com.puntazo.app", RootsPEM: roots}, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.now = func() time.Time { return now }
	return signedFixture{client: c, key: keys[0], issuerKey: keys[1], chain: []string{base64.StdEncoding.EncodeToString(der[0]), base64.StdEncoding.EncodeToString(der[1]), base64.StdEncoding.EncodeToString(der[2])}, now: now}
}
func (f signedFixture) sign(t *testing.T, value any) string {
	t.Helper()
	raw, _ := json.Marshal(value)
	claims := jwt.MapClaims{}
	if e := json.Unmarshal(raw, &claims); e != nil {
		t.Fatal(e)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["x5c"] = f.chain
	s, e := token.SignedString(f.key)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f signedFixture) transaction() model.AppleTransaction {
	return model.AppleTransaction{TransactionID: "2001", OriginalTransactionID: "1001", BundleID: "com.puntazo.app", ProductID: Catalog()[0].ProductID, AppAccountToken: uuid.NewString(), Environment: "Xcode", Type: "Auto-Renewable Subscription", OwnershipType: "PURCHASED", PurchaseDate: f.now.Add(-time.Hour).UnixMilli(), ExpiresDate: f.now.Add(time.Hour).UnixMilli(), SignedDate: f.now.UnixMilli(), Currency: "USD", Price: 4990}
}
func TestSignedTransactionTrustAndContext(t *testing.T) {
	f := certificateFixture(t)
	valid := f.transaction()
	signed := f.sign(t, valid)
	if got, e := f.client.VerifyTransaction(t.Context(), signed); e != nil || got.TransactionID != valid.TransactionID {
		t.Fatalf("verify: %+v %v", got, e)
	}
	for _, change := range []func(*model.AppleTransaction){func(v *model.AppleTransaction) { v.Environment = "Production" }, func(v *model.AppleTransaction) { v.BundleID = "other.app" }, func(v *model.AppleTransaction) { v.ProductID = "unapproved.sku" }, func(v *model.AppleTransaction) { v.OwnershipType = "FAMILY_SHARED" }, func(v *model.AppleTransaction) { v.SignedDate = f.now.Add(time.Hour).UnixMilli() }, func(v *model.AppleTransaction) { v.ExpiresDate = v.PurchaseDate }, func(v *model.AppleTransaction) { v.AppAccountToken = "not-uuid" }} {
		v := valid
		change(&v)
		if _, e := f.client.VerifyTransaction(t.Context(), f.sign(t, v)); e == nil {
			t.Fatalf("accepted invalid %+v", v)
		}
	}
	substituted := f
	attacker := certificateFixture(t)
	substituted.chain = append([]string(nil), f.chain...)
	substituted.chain[2] = attacker.chain[2]
	if _, e := f.client.VerifyTransaction(t.Context(), substituted.sign(t, valid)); e == nil {
		t.Fatal("accepted x5c OCSP issuer outside verified trusted chain")
	}
	other := certificateFixture(t)
	if _, e := other.client.VerifyTransaction(t.Context(), signed); e == nil {
		t.Fatal("accepted attacker root")
	}
	parts := strings.Split(signed, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"signedDate":1}`))
	if _, e := f.client.VerifyTransaction(t.Context(), strings.Join(parts, ".")); e == nil {
		t.Fatal("accepted payload tampering")
	}
	// An operator cannot accidentally carry the local test trust configuration into production.
	if _, e := New(Config{Enabled: true, Production: true, Environment: "Xcode", AllowXcode: true}, nil); e == nil {
		t.Fatal("production accepted Xcode")
	}
}
func TestNotificationNestedSignatures(t *testing.T) {
	f := certificateFixture(t)
	tx := f.transaction()
	n := model.AppleNotification{NotificationUUID: uuid.NewString(), Version: "2.0", NotificationType: "DID_RENEW", SignedDate: f.now.UnixMilli()}
	n.Data.Environment = "Xcode"
	n.Data.BundleID = tx.BundleID
	n.Data.SignedTransaction = f.sign(t, tx)
	r := model.AppleRenewal{OriginalTransactionID: tx.OriginalTransactionID, Environment: "Xcode", SignedDate: f.now.UnixMilli(), AutoRenewStatus: 1}
	n.Data.SignedRenewal = f.sign(t, r)
	got, e := f.client.VerifyNotification(t.Context(), f.sign(t, n))
	if e != nil || got.Transaction == nil || got.Renewal == nil {
		t.Fatalf("notification: %+v %v", got, e)
	}
	r.OriginalTransactionID = "another-chain"
	n.Data.SignedRenewal = f.sign(t, r)
	if _, e = f.client.VerifyNotification(t.Context(), f.sign(t, n)); e == nil {
		t.Fatal("accepted mismatching renewal")
	}
}
func TestServerAPIJWTAndAccountToken(t *testing.T) {
	f := certificateFixture(t)
	f.client.key = f.key
	f.client.cfg.KeyID = "key"
	f.client.cfg.IssuerID = "issuer"
	token := uuid.NewString()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims := jwt.MapClaims{}
		parsed, e := jwt.ParseWithClaims(bearer, claims, func(*jwt.Token) (any, error) { return &f.key.PublicKey, nil }, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("appstoreconnect-v1"), jwt.WithIssuer("issuer"))
		if e != nil || !parsed.Valid {
			t.Errorf("invalid api JWT %v", e)
			w.WriteHeader(401)
			return
		}
		if r.Method != "PUT" || r.URL.Path != "/inApps/v1/transactions/1001/appAccountToken" {
			t.Error("wrong api method/path")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["appAccountToken"] != token {
			t.Error("wrong token assignment")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	f.client.base = server.URL
	if e := f.client.SetAppAccountToken(context.Background(), "1001", token); e != nil || calls != 1 {
		t.Fatalf("assign token %v", e)
	}
}

type appleRoundTrip func(*http.Request) (*http.Response, error)

func (f appleRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestInvalidJWSSignatureMakesNoOCSPRequests(t *testing.T) {
	fixture := certificateFixture(t)
	fixture.client.cfg.Environment = "Production"
	fixture.client.cfg.SubscriptionGroupID = "merchant-group"
	transaction := fixture.transaction()
	transaction.Environment = "Production"
	transaction.SubscriptionGroupID = "merchant-group"
	signed := fixture.sign(t, transaction)
	parts := strings.Split(signed, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	forged := strings.Join(parts, ".")
	var requests atomic.Int32
	fixture.client.http = &http.Client{Transport: appleRoundTrip(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("OCSP must not run for a forged JWS")
	})}
	if _, err := fixture.client.VerifyTransaction(t.Context(), forged); !errors.Is(err, ErrInvalidSignedData) {
		t.Fatalf("invalid signature error=%v", err)
	}
	if count := requests.Load(); count != 0 {
		t.Fatalf("forged signature triggered %d OCSP requests", count)
	}
}
func TestOCSPRequestsCoalesceCacheAndFailClosedAtExpiry(t *testing.T) {
	fixture := certificateFixture(t)
	parse := func(encoded string) *x509.Certificate {
		der, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			t.Fatal(e)
		}
		cert, e := x509.ParseCertificate(der)
		if e != nil {
			t.Fatal(e)
		}
		return cert
	}
	leaf, issuer := parse(fixture.chain[0]), parse(fixture.chain[1])
	nextUpdate := fixture.now.Add(time.Minute)
	encoded, err := ocsp.CreateResponse(issuer, issuer, ocsp.Response{Status: ocsp.Good, SerialNumber: leaf.SerialNumber, ThisUpdate: fixture.now.Add(-time.Minute), NextUpdate: nextUpdate}, fixture.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	leaf.OCSPServer = []string{server.URL}
	const callers = 16
	var wg sync.WaitGroup
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- fixture.client.checkOCSP(context.Background(), leaf, issuer) }()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal("valid OCSP", e)
		}
	}
	if count := requests.Load(); count != 1 {
		t.Fatalf("concurrent requests not coalesced: %d", count)
	}
	if e := fixture.client.checkOCSP(t.Context(), leaf, issuer); e != nil || requests.Load() != 1 {
		t.Fatal("valid OCSP not cached", e)
	}
	// This moves the clock only after every concurrent caller finished.
	fixture.client.now = func() time.Time { return nextUpdate.Add(time.Second) }
	for i := 0; i < 2; i++ {
		if e := fixture.client.checkOCSP(t.Context(), leaf, issuer); !errors.Is(e, ErrInvalidSignedData) {
			t.Fatal("accepted stale OCSP", e)
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("expired/bad OCSP was cached: requests=%d", requests.Load())
	}
}

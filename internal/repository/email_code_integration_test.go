package repository_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/emailcode"
	"clientesFrecuentes/internal/mailer"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var otpTestKey = []byte("01234567890123456789012345678901")

func codeFixture(t *testing.T) (*pgxpool.Pool, *repository.Repository, *service.Service) {
	t.Helper()
	pool := referralBillingPool(t)
	repo := repository.New(pool, otpTestKey)
	cfg := config.Config{DemoSignupEnabled: true, EmailVerificationRequired: true, OutboxEncryptionKey: otpTestKey, PublicAppURL: "https://app.puntazo.test", QRPepper: "test-qr-pepper-01234567890123456789"}
	return pool, repo, service.New(repo, auth.NewTokens("test-jwt-secret-012345678901234567", "puntazo"), cfg)
}
func pendingCode(t *testing.T, svc *service.Service, email string) model.RegisterCustomerData {
	t.Helper()
	data, err := svc.RegisterCustomer(t.Context(), model.RegisterCustomerRequest{Name: "Gabriel", LastName: "Prueba", Email: email, Password: "a-test-password-123", VerificationMethod: "code"})
	if err != nil || !data.VerificationRequired || data.Session != nil || data.VerificationMethod != "code" || len(data.RegistrationToken) < 40 {
		t.Fatalf("pending code registration data=%+v err=%v", data, err)
	}
	return data
}
func queuedCode(t *testing.T, pool *pgxpool.Pool, userID int64) string {
	t.Helper()
	var item model.OutboxEmail
	if err := pool.QueryRow(t.Context(), `SELECT id::text,token_ciphertext,token_nonce FROM email_outbox WHERE usuario_id=$1 AND estado='PENDING' AND tipo='VERIFY_EMAIL' ORDER BY created_at DESC LIMIT 1`, userID).Scan(&item.ID, &item.Ciphertext, &item.Nonce); err != nil {
		t.Fatal(err)
	}
	plain, err := repository.DecryptOutboxToken(item, otpTestKey)
	if err != nil || !strings.HasPrefix(plain, "code:") {
		t.Fatalf("encrypted code payload err=%v", err)
	}
	return strings.TrimPrefix(plain, "code:")
}
func TestPostgresCodeRegistrationAndEnvironment(t *testing.T) {
	pool, _, svc := codeFixture(t)
	ctx := t.Context()
	pending := pendingCode(t, svc, "code-active@example.test")
	code := queuedCode(t, pool, pending.User.ID)
	if _, err := svc.Login(ctx, model.LoginRequest{Email: pending.User.Email, Password: "a-test-password-123"}); !errors.Is(err, service.ErrEmailUnverified) {
		t.Fatalf("pending login err=%v", err)
	}
	// Turning off the flag only affects future registrations.
	svc.Config.EmailVerificationRequired = false
	if _, err := svc.Login(ctx, model.LoginRequest{Email: pending.User.Email, Password: "a-test-password-123"}); !errors.Is(err, service.ErrEmailUnverified) {
		t.Fatalf("flag bypassed old pending account err=%v", err)
	}
	immediate, err := svc.RegisterCustomer(ctx, model.RegisterCustomerRequest{Name: "Directo", LastName: "Prueba", Email: "code-disabled@example.test", Password: "a-test-password-123", VerificationMethod: "code"})
	if err != nil || immediate.VerificationRequired || immediate.Session == nil || !immediate.User.EmailVerified || immediate.RegistrationToken != "" {
		t.Fatalf("disabled registration err=%v", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE usuario_id=$1`, immediate.User.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled enqueued email count=%d err=%v", count, err)
	}
	result, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: code, RegistrationToken: pending.RegistrationToken})
	if err != nil || !result.Verified || result.Session == nil || result.User == nil || !result.User.EmailVerified {
		t.Fatalf("verify/session result=%+v err=%v", result, err)
	}
	if _, err = svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: code, RegistrationToken: pending.RegistrationToken}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatalf("reuse err=%v", err)
	}
}
func TestPostgresCodeAttemptsRotationAndLegacyCompatibility(t *testing.T) {
	pool, repo, svc := codeFixture(t)
	ctx := t.Context()
	pending := pendingCode(t, svc, "code-attempts@example.test")
	oldCode := queuedCode(t, pool, pending.User.ID)
	// Cooldown returns generic success and does not replace queued code.
	if err := svc.RequestEmailVerificationCode(ctx, model.EmailRequest{Email: pending.User.Email}); err != nil {
		t.Fatal(err)
	}
	if got := queuedCode(t, pool, pending.User.ID); got != oldCode {
		t.Fatal("cooldown rotated code")
	}
	wrong := "999999"
	if wrong == oldCode {
		wrong = "000000"
	}
	for range 5 {
		if _, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: wrong}); !errors.Is(err, service.ErrIdentityToken) {
			t.Fatalf("wrong attempt err=%v", err)
		}
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM email_verification_codes WHERE usuario_id=$1`, pending.User.ID).Scan(&attempts); err != nil || attempts != 5 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if _, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: oldCode}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatal("accepted exhausted code")
	}
	if _, err := pool.Exec(ctx, `UPDATE email_verification_codes SET sent_at=now()-interval '61 seconds' WHERE usuario_id=$1`, pending.User.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestEmailVerificationCode(ctx, model.EmailRequest{Email: pending.User.Email}); err != nil {
		t.Fatal(err)
	}
	newCode := queuedCode(t, pool, pending.User.ID)
	if newCode == oldCode {
		t.Fatal("rotation reused code")
	}
	if _, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: oldCode}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatal("old code accepted")
	}
	// Initial context survives resend; scope isolation prevents cross-account code.
	other := pendingCode(t, svc, "code-other@example.test")
	if bytes.Equal(emailcode.Hash(otpTestKey, pending.User.Email, newCode), emailcode.Hash(otpTestKey, other.User.Email, newCode)) {
		t.Fatal("unscoped fingerprint")
	}
	result, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: newCode, RegistrationToken: pending.RegistrationToken})
	if err != nil || result.Session == nil {
		t.Fatalf("rotated context err=%v", err)
	}
	// Old API can issue links; requesting a code rotates that link and emits a new paired link.
	legacy, err := svc.RegisterCustomer(ctx, model.RegisterCustomerRequest{Name: "Legacy", LastName: "Prueba", Email: "code-legacy@example.test", Password: "a-test-password-123"})
	if err != nil {
		t.Fatal(err)
	}
	var oldItem model.OutboxEmail
	if err = pool.QueryRow(ctx, `SELECT id::text,token_ciphertext,token_nonce FROM email_outbox WHERE usuario_id=$1 AND estado='PENDING'`, legacy.User.ID).Scan(&oldItem.ID, &oldItem.Ciphertext, &oldItem.Nonce); err != nil {
		t.Fatal(err)
	}
	oldToken, err := repository.DecryptOutboxToken(oldItem, otpTestKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.RequestEmailVerificationCode(ctx, model.EmailRequest{Email: legacy.User.Email}); err != nil {
		t.Fatal(err)
	}
	var liveLinks int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tokens_identidad_email WHERE usuario_id=$1 AND consumed_at IS NULL`, legacy.User.ID).Scan(&liveLinks); err != nil || liveLinks != 1 {
		t.Fatalf("live links=%d err=%v", liveLinks, err)
	}
	if err = svc.ConfirmEmailVerification(ctx, model.TokenRequest{Token: oldToken}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatal("superseded legacy link accepted", err)
	}
	// Switching back to a link prevents a code confirmation.
	otherCode := queuedCode(t, pool, other.User.ID)
	if err = svc.RequestEmailVerification(ctx, model.EmailRequest{Email: other.User.Email}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: other.User.Email, Code: otherCode}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatal("legacy rotation left code valid")
	}
	if err = repo.CheckSchema(ctx, "0045"); err != nil {
		t.Fatal(err)
	}
}
func TestPostgresCodeExpiryProofAndConcurrentConsume(t *testing.T) {
	pool, _, svc := codeFixture(t)
	ctx := t.Context()
	for _, test := range []struct {
		name                              string
		expireCode, expireProof, badProof bool
	}{{"expired-code", true, false, false}, {"expired-proof", false, true, false}, {"wrong-proof", false, false, true}, {"no-proof", false, false, false}} {
		pending := pendingCode(t, svc, test.name+"@example.test")
		code := queuedCode(t, pool, pending.User.ID)
		proof := ""
		if test.expireCode || test.expireProof {
			proof = pending.RegistrationToken
		}
		if test.badProof {
			proof = strings.Repeat("x", 43)
		}
		if test.expireCode {
			_, _ = pool.Exec(ctx, `UPDATE email_verification_codes SET expires_at=now()-interval '1 second' WHERE usuario_id=$1`, pending.User.ID)
		}
		if test.expireProof {
			_, _ = pool.Exec(ctx, `UPDATE email_verification_codes SET registration_expires_at=now()-interval '1 second' WHERE usuario_id=$1`, pending.User.ID)
		}
		result, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: code, RegistrationToken: proof})
		if test.expireCode {
			if !errors.Is(err, service.ErrIdentityToken) {
				t.Fatalf("expired err=%v", err)
			}
		} else if err != nil || !result.Verified || result.Session != nil {
			t.Fatalf("proof fallback result=%+v err=%v", result, err)
		}
	}
	pending := pendingCode(t, svc, "concurrent-code@example.test")
	code := queuedCode(t, pool, pending.User.ID)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: pending.User.Email, Code: code, RegistrationToken: pending.RegistrationToken})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, service.ErrIdentityToken) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes=%d", successes)
	}
}
func TestPostgresMerchantCodeIdempotencyAndDeliveryValidation(t *testing.T) {
	pool, repo, svc := codeFixture(t)
	ctx := t.Context()
	key := uuid.NewString()
	req := model.RegisterDemoMerchantRequest{Email: "code-merchant@example.test", Password: "merchant-password-123", OwnerName: "Gabriel", OwnerLastName: "Prueba", BrandName: "Comercio Código", BranchName: "Principal", ProgramType: "SELLOS", VerificationMethod: "code"}
	first, err := svc.RegisterDemoMerchant(ctx, key, uuid.NewString(), req)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data model.DemoMerchantData `json:"data"`
	}
	if err = json.Unmarshal(first.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	pending := envelope.Data
	if pending.Session != nil || !pending.VerificationRequired || pending.VerificationMethod != "code" || pending.RegistrationToken == "" {
		t.Fatal("merchant pending response")
	}
	var persisted []byte
	if err = pool.QueryRow(ctx, `SELECT response_body FROM solicitudes_idempotentes WHERE idempotency_key=$1`, key).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(pending.RegistrationToken)) || bytes.Contains(persisted, []byte("registration_token")) {
		t.Fatal("idempotency persisted bearer proof")
	}
	repeated, err := svc.RegisterDemoMerchant(ctx, key, uuid.NewString(), req)
	if err != nil || !repeated.Replayed {
		t.Fatalf("merchant replay err=%v", err)
	}
	if err = json.Unmarshal(repeated.Body, &envelope); err != nil || envelope.Data.RegistrationToken != pending.RegistrationToken {
		t.Fatal("merchant proof not stable")
	}
	changed := req
	changed.VerificationMethod = ""
	if _, err = svc.RegisterDemoMerchant(ctx, key, uuid.NewString(), changed); err == nil {
		t.Fatal("changed verification method idempotency accepted")
	}
	items, err := repo.ClaimEmails(ctx, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim count=%d err=%v", len(items), err)
	}
	payload, err := repository.DecryptOutboxToken(items[0], otpTestKey)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimPrefix(payload, "code:")
	if err = repo.ValidateClaimedEmail(ctx, items[0], payload); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: req.Email, Code: code, RegistrationToken: pending.RegistrationToken})
	if err != nil || result.Session == nil {
		t.Fatalf("merchant confirm err=%v", err)
	}
	if err = repo.ValidateClaimedEmail(ctx, items[0], payload); !errors.Is(err, repository.ErrIdentityTokenInvalid) {
		t.Fatal("delivery accepted consumed code")
	}
	// Unknown and verified addresses remain indistinguishable at request service.
	for _, email := range []string{"unknown@example.test", req.Email} {
		if err = svc.RequestEmailVerificationCode(ctx, model.EmailRequest{Email: email}); err != nil {
			t.Fatal(err)
		}
	}
	// A manually queued message with leading zeroes proves storage remains textual.
	other := pendingCode(t, svc, "leading-zero@example.test")
	if _, err = pool.Exec(ctx, `UPDATE email_verification_codes SET sent_at=now()-interval '61 seconds' WHERE usuario_id=$1`, other.User.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.EnqueueVerificationCode(ctx, other.User.Email, "000001", time.Now().Add(10*time.Minute), mailer.VerificationCodeMessage(other.User.Email, "000001")); err != nil {
		t.Fatal(err)
	}
	if got := queuedCode(t, pool, other.User.ID); got != "000001" {
		t.Fatal("leading zeroes lost")
	}
}

func TestPostgresCodeMigrationUpgradeAndReadiness(t *testing.T) {
	pool, repo, svc := codeFixture(t)
	ctx := t.Context()
	pending := pendingCode(t, svc, "migration-code@example.test")
	apply := func(direction string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0043_email_verification_codes."+direction+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	apply("down")
	var cancelled bool
	if err := pool.QueryRow(ctx, `SELECT estado='FAILED' AND token_ciphertext IS NULL FROM email_outbox WHERE usuario_id=$1`, pending.User.ID).Scan(&cancelled); err != nil || !cancelled {
		t.Fatalf("rollback cancellation=%v err=%v", cancelled, err)
	}
	apply("up")
	if err := repo.CheckSchema(ctx, "0045"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0008'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckSchema(ctx, "0045"); err == nil {
		t.Fatal("accepted missing migration despite latest version matching")
	}
}

func TestPostgresLegacyMerchantIdempotencyFingerprintUnchanged(t *testing.T) {
	pool, _, svc := codeFixture(t)
	ctx := t.Context()
	key := uuid.NewString()
	req := model.RegisterDemoMerchantRequest{Email: "legacy-idempotency@example.test", Password: "merchant-password-123", OwnerName: "Gabriel", OwnerLastName: "Prueba", BrandName: "Legacy", BranchName: "Principal", ProgramType: "SELLOS"}
	if _, err := svc.RegisterDemoMerchant(ctx, key, uuid.NewString(), req); err != nil {
		t.Fatal(err)
	}
	// Exact JSON of the pre-code request fingerprint: no new empty fields.
	previousJSON := `{"Email":"legacy-idempotency@example.test","Password":"merchant-password-123","OwnerName":"Gabriel","OwnerLastName":"Prueba","BrandName":"Legacy","BranchName":"Principal","BranchAddress":null,"ProgramType":"SELLOS","ReferralCode":""}`
	mac := hmac.New(sha256.New, []byte(svc.Config.QRPepper))
	mac.Write([]byte(previousJSON))
	expected := mac.Sum(nil)
	var got []byte
	if err := pool.QueryRow(ctx, `SELECT fingerprint FROM solicitudes_idempotentes WHERE idempotency_key=$1`, key).Scan(&got); err != nil || !hmac.Equal(got, expected) {
		t.Fatalf("historical fingerprint changed err=%v", err)
	}
	replay, err := svc.RegisterDemoMerchant(ctx, key, uuid.NewString(), req)
	if err != nil || !replay.Replayed {
		t.Fatalf("historical replay err=%v", err)
	}
}

// Older migration upgrade fixtures start from the current source schema.
func rollbackCodeMigrationFixture(t *testing.T, p *pgxpool.Pool) {
	rollbackAppleMigrationFixture(t, p)
	t.Helper()
	body, err := os.ReadFile("../../migrations/0043_email_verification_codes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version='0043'`); err != nil {
		t.Fatal(err)
	}
}

func codeLink(t *testing.T, pool *pgxpool.Pool, email string, id int64, code string) string {
	t.Helper()
	var deliveryID string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM email_outbox WHERE usuario_id=$1 AND estado='PENDING' ORDER BY created_at DESC LIMIT 1`, id).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	return emailcode.ActivationToken(otpTestKey, email, code, deliveryID)
}
func TestPostgresCodeAndLinkShareConsumption(t *testing.T) {
	pool, _, svc := codeFixture(t)
	ctx := t.Context()
	for _, first := range []string{"code", "link", "concurrent", "resend", "attempts", "expired"} {
		p := pendingCode(t, svc, "dual-"+first+"@example.test")
		code := queuedCode(t, pool, p.User.ID)
		token := codeLink(t, pool, p.User.Email, p.User.ID, code)
		confirmCode := func() error {
			_, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: p.User.Email, Code: code})
			return err
		}
		confirmLink := func() error { return svc.ConfirmEmailVerification(ctx, model.TokenRequest{Token: token}) }
		switch first {
		case "code":
			if err := confirmCode(); err != nil {
				t.Fatal(err)
			}
		case "link":
			if err := confirmLink(); err != nil {
				t.Fatal(err)
			}
		case "concurrent":
			results := make(chan error, 2)
			go func() { results <- confirmCode() }()
			go func() { results <- confirmLink() }()
			success := 0
			for range 2 {
				err := <-results
				if err == nil {
					success++
				} else if !errors.Is(err, service.ErrIdentityToken) {
					t.Fatal(err)
				}
			}
			if success != 1 {
				t.Fatalf("concurrent successes=%d", success)
			}
		case "resend":
			if _, err := pool.Exec(ctx, `UPDATE email_verification_codes SET sent_at=now()-interval '61 seconds' WHERE usuario_id=$1`, p.User.ID); err != nil {
				t.Fatal(err)
			}
			if err := svc.RequestEmailVerificationCode(ctx, model.EmailRequest{Email: p.User.Email}); err != nil {
				t.Fatal(err)
			}
		case "attempts":
			wrong := "999999"
			if wrong == code {
				wrong = "000000"
			}
			for range 5 {
				_, err := svc.ConfirmEmailVerificationCode(ctx, model.VerificationCodeConfirmRequest{Email: p.User.Email, Code: wrong})
				if !errors.Is(err, service.ErrIdentityToken) {
					t.Fatal(err)
				}
			}
		case "expired":
			svc.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
		}
		if err := confirmCode(); !errors.Is(err, service.ErrIdentityToken) {
			t.Fatalf("%s code reuse=%v", first, err)
		}
		if err := confirmLink(); !errors.Is(err, service.ErrIdentityToken) {
			t.Fatalf("%s link reuse=%v", first, err)
		}
		svc.Now = time.Now
	}
}

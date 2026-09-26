package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresDemoSellosLifecycle(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS citext`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0001_demo_sellos.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	migration := strings.Replace(string(sql), "CREATE EXTENSION IF NOT EXISTS citext;", "", 1)
	if _, err = pool.Exec(ctx, migration); err != nil {
		t.Fatalf("migration: %v", err)
	}
	programMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0002_program_types.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(programMigration)); err != nil {
		t.Fatalf("migration 0002: %v", err)
	}
	tenantMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0003_tenant_roles.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(tenantMigration)); err != nil {
		t.Fatalf("migration 0003: %v", err)
	}
	sessionMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0004_auth_sessions.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sessionMigration)); err != nil {
		t.Fatalf("migration 0004: %v", err)
	}
	benefitMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0005_benefit_versions.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(benefitMigration)); err != nil {
		t.Fatalf("migration 0005: %v", err)
	}
	familyMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0006_session_families.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(familyMigration)); err != nil {
		t.Fatalf("migration 0006: %v", err)
	}
	snapshotMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0007_movement_snapshots.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(snapshotMigration)); err != nil {
		t.Fatalf("migration 0007: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta) VALUES('legacy-password@example.com','legacy-hash','Legacy password','PERSONAL_MARCA'); INSERT INTO usuarios(email,google_id,nombre,tipo_cuenta,qr_hash) VALUES('legacy-google@example.com','legacy-google','Legacy Google','CLIENTE_FINAL',decode('01','hex')); INSERT INTO sesiones_auth(id,usuario_id,refresh_hash,expires_at,family_id,auth_time) SELECT '00000000-0000-4000-8000-000000000001',id,decode(repeat('02',32),'hex'),now()+interval '30 days','00000000-0000-4000-8000-000000000001',now() FROM usuarios WHERE email='legacy-password@example.com'`); err != nil {
		t.Fatalf("legacy fixtures: %v", err)
	}
	identityMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0008_email_identity.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(identityMigration)); err != nil {
		t.Fatalf("migration 0008: %v", err)
	}
	accountMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0009_account_lifecycle.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(accountMigration)); err != nil {
		t.Fatalf("migration 0009: %v", err)
	}
	secureOutboxMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0010_secure_email_outbox.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(secureOutboxMigration)); err != nil {
		t.Fatalf("migration 0010: %v", err)
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE schema_migrations(version CHAR(4) PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now()); INSERT INTO schema_migrations(version) VALUES('0001'),('0002'),('0003'),('0004'),('0005'),('0006'),('0007'),('0008'),('0009'),('0010')`); err != nil {
		t.Fatal(err)
	}
	legacyFixMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0011_correct_legacy_verification.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(legacyFixMigration)); err != nil {
		t.Fatalf("migration 0011: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0011')`); err != nil {
		t.Fatal(err)
	}
	commercialMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0012_commercial_editing.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(commercialMigration)); err != nil {
		t.Fatalf("migration 0012: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0012')`); err != nil {
		t.Fatal(err)
	}
	staffMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0013_brand_staff_invitations.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(staffMigration)); err != nil {
		t.Fatalf("migration 0013: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0013')`); err != nil {
		t.Fatal(err)
	}
	outboxCorrelationMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0014_invitation_outbox_correlation.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(outboxCorrelationMigration)); err != nil {
		t.Fatalf("migration 0014: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0014')`); err != nil {
		t.Fatal(err)
	}
	mediaMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0015_private_brand_media.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(mediaMigration)); err != nil {
		t.Fatalf("migration 0015: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0015')`); err != nil {
		t.Fatal(err)
	}
	retentionMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0016_operational_retention.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(retentionMigration)); err != nil {
		t.Fatalf("migration 0016: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0016')`); err != nil {
		t.Fatal(err)
	}
	reconciliationMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0017_media_reconciliation.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(reconciliationMigration)); err != nil {
		t.Fatalf("migration 0017: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0017')`); err != nil {
		t.Fatal(err)
	}
	cardDesignMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0018_brand_card_design.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(cardDesignMigration)); err != nil {
		t.Fatalf("migration 0018: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0018')`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0019", "0020", "0021"} {
		migration, readErr := os.ReadFile(filepath.Join("..", "..", "migrations", version+map[string]string{"0019": "_mercado_pago_subscriptions", "0020": "_subscription_checkout_reservation", "0021": "_expo_push"}[version]+".up.sql"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("migration %s: %v", version, err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
			t.Fatal(err)
		}
	}
	var legacyPasswordVerified, legacyGoogleVerified bool
	if err = pool.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM usuarios WHERE email='legacy-password@example.com'`).Scan(&legacyPasswordVerified); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM usuarios WHERE email='legacy-google@example.com'`).Scan(&legacyGoogleVerified); err != nil {
		t.Fatal(err)
	}
	if legacyPasswordVerified || !legacyGoogleVerified {
		t.Fatalf("legacy verification password=%t google=%t", legacyPasswordVerified, legacyGoogleVerified)
	}
	var legacySessionRevoked bool
	if err = pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM sesiones_auth WHERE id='00000000-0000-4000-8000-000000000001'`).Scan(&legacySessionRevoked); err != nil || !legacySessionRevoked {
		t.Fatalf("legacy session revoked=%t err=%v", legacySessionRevoked, err)
	}
	outboxKey := []byte("01234567890123456789012345678901")
	cfg := config.Config{JWTSecret: "jwt-secret-0123456789012345678901", JWTIssuer: "puntazo", QRPepper: "qr-pepper-01234567890123456789012", DemoSignupEnabled: true, ExpectedSchemaVersion: "0021", PublicAppURL: "https://app.puntazo.test", OutboxEncryptionKey: outboxKey, MediaURLTTL: 5 * time.Minute, MercadoPagoBranchPrice: 12300, MercadoPagoPointsPrice: 45600}
	repo := repository.New(pool, outboxKey)
	blockedGoogleID := "blocked-google-signup"
	blockedGoogleEmail := "blocked-google@example.com"
	if _, err = repo.LoginGoogle(ctx, blockedGoogleID, blockedGoogleEmail, "Blocked Google", false, make([]byte, 32), func(int64) []byte { return make([]byte, 32) }); !errors.Is(err, repository.ErrSignupDisabled) {
		t.Fatalf("disabled Google signup error=%v", err)
	}
	var blockedGoogleUsers int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM usuarios WHERE email=$1 OR google_id=$2`, blockedGoogleEmail, blockedGoogleID).Scan(&blockedGoogleUsers); err != nil || blockedGoogleUsers != 0 {
		t.Fatalf("disabled Google signup created users=%d err=%v", blockedGoogleUsers, err)
	}
	if _, err = repo.LoginGoogle(ctx, "legacy-google", "legacy-google@example.com", "Legacy Google", false, make([]byte, 32), func(int64) []byte { return make([]byte, 32) }); err != nil {
		t.Fatalf("existing Google login blocked while signup disabled: %v", err)
	}
	if _, err = repo.LoginGoogle(ctx, "legacy-password-google", "legacy-password@example.com", "Legacy password", false, make([]byte, 32), func(int64) []byte { return make([]byte, 32) }); err != nil {
		t.Fatalf("existing email link blocked while signup disabled: %v", err)
	}
	var linkedGoogleID string
	var linkedEmailVerified bool
	if err = pool.QueryRow(ctx, `SELECT google_id,email_verified_at IS NOT NULL FROM usuarios WHERE email='legacy-password@example.com'`).Scan(&linkedGoogleID, &linkedEmailVerified); err != nil || linkedGoogleID != "legacy-password-google" || !linkedEmailVerified {
		t.Fatalf("existing email link google_id=%q verified=%t err=%v", linkedGoogleID, linkedEmailVerified, err)
	}
	tokens := auth.NewTokens(cfg.JWTSecret, cfg.JWTIssuer)
	media := &fakeMediaStore{objects: map[string][]byte{}}
	svc := service.New(repo, tokens, cfg, media)
	svc.VerifyGoogleToken = func(_ context.Context, token string) (string, string, string, error) {
		return "gid-" + token, token + "@example.com", "Google " + token, nil
	}
	clientAccountType := "CLIENTE_FINAL"
	merchantAccountType := "PERSONAL_MARCA"
	type googleProvisioningState struct {
		users, sessions, brands, memberships, branches, branchMemberships, programs, benefits, demoAccesses, cards int
	}
	provisioningState := func() googleProvisioningState {
		t.Helper()
		var state googleProvisioningState
		err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM usuarios),
			(SELECT count(*) FROM sesiones_auth),
			(SELECT count(*) FROM marcas),
			(SELECT count(*) FROM membresias_marca),
			(SELECT count(*) FROM sucursales),
			(SELECT count(*) FROM membresias_sucursales),
			(SELECT count(*) FROM programas_fidelidad),
			(SELECT count(*) FROM beneficios),
			(SELECT count(*) FROM accesos_demo),
			(SELECT count(*) FROM tarjetas)`).Scan(
			&state.users, &state.sessions, &state.brands, &state.memberships,
			&state.branches, &state.branchMemberships, &state.programs,
			&state.benefits, &state.demoAccesses, &state.cards,
		)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	beforeAccountSelection := provisioningState()
	if _, err = svc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "needs-type"}); !errors.Is(err, service.ErrAccountTypeRequired) {
		t.Fatalf("new Google account without type error=%v", err)
	}
	if afterAccountSelection := provisioningState(); afterAccountSelection != beforeAccountSelection {
		t.Fatalf("missing account type changed provisioning state: before=%+v after=%+v", beforeAccountSelection, afterAccountSelection)
	}
	googleClient, err := svc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "new-client", AccountType: &clientAccountType})
	if err != nil || googleClient.User.AccountType != "CLIENTE_FINAL" || !googleClient.User.EmailVerified {
		t.Fatalf("Google client=%+v err=%v", googleClient.User, err)
	}
	// Selection fields are ignored for an existing account and cannot convert it.
	existingClient, err := svc.LoginGoogle(ctx, model.GoogleAuthRequest{
		IDToken:     "new-client",
		AccountType: &merchantAccountType,
		MerchantRegistration: &model.GoogleMerchantRegistration{
			BrandName: "Must be ignored", ProgramType: "INVALID",
		},
	})
	if err != nil || existingClient.User.ID != googleClient.User.ID || existingClient.User.AccountType != "CLIENTE_FINAL" {
		t.Fatalf("existing Google client=%+v err=%v", existingClient.User, err)
	}
	badMerchant := &model.GoogleMerchantRegistration{BrandName: "Bad Google Brand", ProgramType: "SELLOS"}
	if _, err = svc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "bad-merchant", AccountType: &merchantAccountType, MerchantRegistration: badMerchant}); !errors.Is(err, service.ErrInvalidRequest) {
		t.Fatalf("Google merchant invalid registration error=%v", err)
	}
	googleAddress, googleLocality, googleProvince, googlePostalCode := "Av. Corrientes 1234", "Buenos Aires", "Ciudad Autónoma de Buenos Aires", "C1043"
	googleLatitude, googleLongitude := -34.603722, -58.381592
	googleMerchantRegistration := &model.GoogleMerchantRegistration{
		BrandName: "Google Brand", BranchName: "Casa Central", BranchAddress: &googleAddress, BranchLocality: &googleLocality,
		BranchProvince: &googleProvince, BranchPostalCode: &googlePostalCode, BranchLatitude: &googleLatitude, BranchLongitude: &googleLongitude,
		ProgramType: "PUNTOS",
	}
	googleMerchant, err := svc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "new-merchant", AccountType: &merchantAccountType, MerchantRegistration: googleMerchantRegistration})
	if err != nil || googleMerchant.User.AccountType != "PERSONAL_MARCA" || !googleMerchant.User.EmailVerified {
		t.Fatalf("Google merchant=%+v err=%v", googleMerchant.User, err)
	}
	var googleMerchantGraph int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM usuarios u
		JOIN membresias_marca mm ON mm.usuario_id=u.id AND mm.rol='PROPIETARIO'
		JOIN marcas m ON m.id=mm.marca_id AND m.nombre='Google Brand'
		JOIN sucursales s ON s.marca_id=m.id AND s.nombre='Casa Central' AND s.principal
			AND s.direccion='Av. Corrientes 1234' AND s.localidad='Buenos Aires' AND s.provincia='Ciudad Autónoma de Buenos Aires'
			AND s.codigo_postal='C1043' AND s.latitud=-34.603722 AND s.longitud=-58.381592
		JOIN programas_fidelidad p ON p.marca_id=m.id AND p.tipo='PUNTOS'
		JOIN accesos_demo ad ON ad.marca_id=m.id AND ad.precio_minor=0 AND NOT ad.cobro_automatico
		WHERE u.id=$1 AND u.password_hash IS NULL AND u.google_id='gid-new-merchant' AND u.email_verified_at IS NOT NULL`, googleMerchant.User.ID).Scan(&googleMerchantGraph); err != nil || googleMerchantGraph != 1 {
		t.Fatalf("Google merchant graph=%d err=%v", googleMerchantGraph, err)
	}
	existingMerchant, err := svc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "new-merchant", AccountType: &clientAccountType})
	if err != nil || existingMerchant.User.ID != googleMerchant.User.ID || existingMerchant.User.AccountType != "PERSONAL_MARCA" {
		t.Fatalf("existing Google merchant=%+v err=%v", existingMerchant.User, err)
	}
	disabledGoogleSvc := service.New(repo, tokens, config.Config{DemoSignupEnabled: false, QRPepper: cfg.QRPepper})
	disabledGoogleSvc.VerifyGoogleToken = svc.VerifyGoogleToken
	if _, err = disabledGoogleSvc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "new-client"}); err != nil {
		t.Fatalf("existing Google login while signup disabled: %v", err)
	}
	if _, err = disabledGoogleSvc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "disabled-new", AccountType: &merchantAccountType, MerchantRegistration: googleMerchantRegistration}); !errors.Is(err, service.ErrDemoDisabled) {
		t.Fatalf("disabled new Google signup error=%v", err)
	}
	if _, err = repo.CreateGoogleMerchant(ctx, "gid-rollback", "rollback-google@example.com", "Rollback", "Rollback Brand", "Principal", model.BranchRegistrationLocation{}, "INVALID"); err == nil {
		t.Fatal("invalid direct Google merchant provision unexpectedly succeeded")
	}
	var rollbackUsers, rollbackBrands int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM usuarios WHERE email='rollback-google@example.com'`).Scan(&rollbackUsers); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM marcas WHERE nombre='Rollback Brand'`).Scan(&rollbackBrands); err != nil || rollbackUsers != 0 || rollbackBrands != 0 {
		t.Fatalf("rolled back users=%d brands=%d err=%v", rollbackUsers, rollbackBrands, err)
	}
	raceRegistration := &model.GoogleMerchantRegistration{BrandName: "Google Race Brand", BranchName: "Principal", ProgramType: "SELLOS"}
	startGoogleRace := make(chan struct{})
	googleRaceErrors := make(chan error, 8)
	var googleRace sync.WaitGroup
	for i := 0; i < 8; i++ {
		googleRace.Add(1)
		go func() {
			defer googleRace.Done()
			<-startGoogleRace
			result, loginErr := svc.LoginGoogle(ctx, model.GoogleAuthRequest{IDToken: "race-merchant", AccountType: &merchantAccountType, MerchantRegistration: raceRegistration})
			if loginErr == nil && result.User.AccountType != "PERSONAL_MARCA" {
				loginErr = fmt.Errorf("unexpected account type %q", result.User.AccountType)
			}
			googleRaceErrors <- loginErr
		}()
	}
	close(startGoogleRace)
	googleRace.Wait()
	close(googleRaceErrors)
	for raceErr := range googleRaceErrors {
		if raceErr != nil {
			t.Fatalf("concurrent Google merchant signup: %v", raceErr)
		}
	}
	var raceUsers, raceBrands, raceMemberships, raceBranches, racePrograms, raceAccesses int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM usuarios WHERE email='race-merchant@example.com'`).Scan(&raceUsers); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM marcas WHERE nombre='Google Race Brand'`).Scan(&raceBrands); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(DISTINCT mm.id),count(DISTINCT s.id),count(DISTINCT p.id),count(DISTINCT ad.id)
		FROM marcas m JOIN membresias_marca mm ON mm.marca_id=m.id JOIN sucursales s ON s.marca_id=m.id
		JOIN programas_fidelidad p ON p.marca_id=m.id JOIN accesos_demo ad ON ad.marca_id=m.id WHERE m.nombre='Google Race Brand'`).
		Scan(&raceMemberships, &raceBranches, &racePrograms, &raceAccesses); err != nil {
		t.Fatal(err)
	}
	if raceUsers != 1 || raceBrands != 1 || raceMemberships != 1 || raceBranches != 1 || racePrograms != 1 || raceAccesses != 1 {
		t.Fatalf("race graph users=%d brands=%d memberships=%d branches=%d programs=%d accesses=%d", raceUsers, raceBrands, raceMemberships, raceBranches, racePrograms, raceAccesses)
	}
	identityCfg := cfg
	identityCfg.EmailVerificationRequired = true
	identitySvc := service.New(repo, tokens, identityCfg)
	pendingRegistration, err := identitySvc.RegisterCustomer(ctx, model.RegisterCustomerRequest{Email: "pending@example.com", Password: "pending-pass", Name: "Pending"})
	if err != nil || pendingRegistration.Session != nil || !pendingRegistration.VerificationRequired {
		t.Fatalf("pending registration=%+v err=%v", pendingRegistration, err)
	}
	if _, err = identitySvc.Login(ctx, model.LoginRequest{Email: "pending@example.com", Password: "pending-pass"}); !errors.Is(err, service.ErrEmailUnverified) {
		t.Fatalf("unverified login: %v", err)
	}
	var verificationOutbox model.OutboxEmail
	if err = pool.QueryRow(ctx, `SELECT id::text,token_ciphertext,token_nonce FROM email_outbox WHERE destinatario='pending@example.com' AND tipo='VERIFY_EMAIL' ORDER BY created_at DESC LIMIT 1`).Scan(&verificationOutbox.ID, &verificationOutbox.Ciphertext, &verificationOutbox.Nonce); err != nil {
		t.Fatal(err)
	}
	verificationToken, err := repository.DecryptOutboxToken(verificationOutbox, outboxKey)
	if err != nil {
		t.Fatal(err)
	}
	var logicalOutbox string
	if err = pool.QueryRow(ctx, `SELECT row_to_json(e)::text FROM email_outbox e WHERE id=$1`, verificationOutbox.ID).Scan(&logicalOutbox); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logicalOutbox, verificationToken) || strings.Contains(logicalOutbox, "?token=") {
		t.Fatal("logical outbox export exposed the verification token")
	}
	if err = identitySvc.ConfirmEmailVerification(ctx, model.TokenRequest{Token: verificationToken}); err != nil {
		t.Fatal(err)
	}
	if err = identitySvc.ConfirmEmailVerification(ctx, model.TokenRequest{Token: verificationToken}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatalf("verification token reused: %v", err)
	}
	pendingLogin, err := identitySvc.Login(ctx, model.LoginRequest{Email: "pending@example.com", Password: "pending-pass"})
	if err != nil {
		t.Fatalf("verified login: %v", err)
	}
	if err = identitySvc.RequestPasswordReset(ctx, model.EmailRequest{Email: "pending@example.com"}); err != nil {
		t.Fatal(err)
	}
	var resetOutbox model.OutboxEmail
	if err = pool.QueryRow(ctx, `SELECT id::text,token_ciphertext,token_nonce FROM email_outbox WHERE destinatario='pending@example.com' AND tipo='RESET_PASSWORD' ORDER BY created_at DESC LIMIT 1`).Scan(&resetOutbox.ID, &resetOutbox.Ciphertext, &resetOutbox.Nonce); err != nil {
		t.Fatal(err)
	}
	resetToken, err := repository.DecryptOutboxToken(resetOutbox, outboxKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = identitySvc.ConfirmPasswordReset(ctx, model.PasswordResetConfirmRequest{Token: resetToken, NewPassword: "new-pending-pass"}); err != nil {
		t.Fatal(err)
	}
	if w := authorizedRequestFor(tokens, repo, pendingLogin.Session.AccessToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("reset did not revoke access: %d", w.Code)
	}
	if err = identitySvc.ConfirmPasswordReset(ctx, model.PasswordResetConfirmRequest{Token: resetToken, NewPassword: "another-password"}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatalf("reset reused: %v", err)
	}
	if _, err = identitySvc.Login(ctx, model.LoginRequest{Email: "pending@example.com", Password: "pending-pass"}); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("old password login: %v", err)
	}
	if _, err = identitySvc.Login(ctx, model.LoginRequest{Email: "pending@example.com", Password: "new-pending-pass"}); err != nil {
		t.Fatalf("new password login: %v", err)
	}

	customerAuth, err := svc.RegisterCustomer(ctx, model.RegisterCustomerRequest{Email: "client@example.com", Password: "customer-pass", Name: "Client"})
	if err != nil {
		t.Fatal(err)
	}
	if customerAuth.Session == nil {
		t.Fatal("registration omitted session while verification is disabled")
	}
	gin.SetMode(gin.TestMode)
	protected := gin.New()
	protected.GET("/protected", middleware.RequireAuth(tokens, repo), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	authorizedRequest := func(accessToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, req)
		return w
	}
	if w := authorizedRequest(customerAuth.Session.AccessToken); w.Code != http.StatusNoContent {
		t.Fatalf("active token status=%d body=%s", w.Code, w.Body.String())
	}
	originalAccess := customerAuth.Session.AccessToken
	originalRefresh := customerAuth.Session.RefreshToken
	refreshedAuth, err := svc.Refresh(ctx, customerAuth.Session.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	customerAuth.Session = &refreshedAuth.Session
	customerAuth.User = refreshedAuth.User
	if w := authorizedRequest(originalAccess); w.Code != http.StatusUnauthorized {
		t.Fatalf("rotated access token status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = svc.Refresh(ctx, originalRefresh); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("refresh token replay: %v", err)
	}
	if w := authorizedRequest(customerAuth.Session.AccessToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("session family access after reuse status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = svc.Refresh(ctx, customerAuth.Session.RefreshToken); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("session family refresh after reuse: %v", err)
	}
	loggedInAuth, err := svc.Login(ctx, model.LoginRequest{Email: "client@example.com", Password: "customer-pass"})
	if err != nil {
		t.Fatal(err)
	}
	customerAuth.Session = &loggedInAuth.Session
	customerAuth.User = loggedInAuth.User
	if _, err = pool.Exec(ctx, `UPDATE usuarios SET activo=false WHERE id=$1`, customerAuth.User.ID); err != nil {
		t.Fatal(err)
	}
	w := authorizedRequest(customerAuth.Session.AccessToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("suspended token status=%d body=%s", w.Code, w.Body.String())
	}
	var suspensionEnvelope web.ErrorEnvelope
	if err = json.Unmarshal(w.Body.Bytes(), &suspensionEnvelope); err != nil {
		t.Fatal(err)
	}
	if suspensionEnvelope.Error.Code != "UNAUTHENTICATED" {
		t.Fatalf("suspended error=%+v", suspensionEnvelope.Error)
	}
	if _, err = pool.Exec(ctx, `UPDATE usuarios SET activo=true WHERE id=$1`, customerAuth.User.ID); err != nil {
		t.Fatal(err)
	}
	_, _, sessionID, err := tokens.ParseSession(customerAuth.Session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Logout(ctx, customerAuth.User.ID, sessionID); err != nil {
		t.Fatal(err)
	}
	if w = authorizedRequest(customerAuth.Session.AccessToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("logged out token status=%d body=%s", w.Code, w.Body.String())
	}
	customer, err := svc.Customer(ctx, customerAuth.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	var storedHash []byte
	if err = pool.QueryRow(ctx, `SELECT qr_hash FROM usuarios WHERE id=$1`, customer.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(storedHash, []byte(customer.QRToken)) || !bytes.Equal(storedHash, svc.QRHash(customer.QRToken)) {
		t.Fatal("QR was not stored exclusively as the expected hash")
	}
	if _, err = svc.RegisterCustomer(ctx, model.RegisterCustomerRequest{Email: "client@example.com", Password: "customer-pass", Name: "Duplicate"}); !errors.Is(err, repository.ErrEmailExists) {
		t.Fatalf("duplicate email: %v", err)
	}

	merchantAddress, merchantLocality, merchantProvince, merchantPostalCode := "San Martín 100", "Mendoza", "Mendoza", "M5500"
	merchantLatitude, merchantLongitude := -32.8894587, -68.8458386
	merchantReq := model.RegisterDemoMerchantRequest{
		Email: "owner@example.com", Password: "merchant-pass", OwnerName: "Owner", BrandName: "Brand", BranchName: "Main",
		BranchAddress: &merchantAddress, BranchLocality: &merchantLocality, BranchProvince: &merchantProvince, BranchPostalCode: &merchantPostalCode,
		BranchLatitude: &merchantLatitude, BranchLongitude: &merchantLongitude, ProgramType: "SELLOS",
	}
	merchantKey := uuid.NewString()
	created, err := svc.RegisterDemoMerchant(ctx, merchantKey, uuid.NewString(), merchantReq)
	if err != nil {
		t.Fatal(err)
	}
	var createdEnvelope web.Envelope[model.DemoMerchantData]
	if err = json.Unmarshal(created.Body, &createdEnvelope); err != nil {
		t.Fatal(err)
	}
	merchant := createdEnvelope.Data
	if merchant.OnboardingComplete || merchant.Merchant.Benefit != nil || len(merchant.Merchant.Benefits) != 0 {
		t.Fatalf("merchant signup invented onboarding data: %+v", merchant)
	}
	if merchant.Merchant.Branch.Address == nil || *merchant.Merchant.Branch.Address != merchantAddress || merchant.Merchant.Branch.Locality == nil || *merchant.Merchant.Branch.Locality != merchantLocality || merchant.Merchant.Branch.Latitude == nil || *merchant.Merchant.Branch.Latitude != merchantLatitude || merchant.Merchant.Branch.Longitude == nil || *merchant.Merchant.Branch.Longitude != merchantLongitude {
		t.Fatalf("merchant signup lost branch location: %+v", merchant.Merchant.Branch)
	}
	replayed, err := svc.RegisterDemoMerchant(ctx, merchantKey, uuid.NewString(), merchantReq)
	if err != nil || !replayed.Replayed || bytes.Equal(created.Body, replayed.Body) {
		t.Fatalf("merchant replay: replay=%v err=%v", replayed.Replayed, err)
	}
	var persistedMerchantResponse []byte
	if err = pool.QueryRow(ctx, `SELECT response_body FROM solicitudes_idempotentes WHERE idempotency_key=$1`, merchantKey).Scan(&persistedMerchantResponse); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persistedMerchantResponse, []byte("refresh_token")) || bytes.Contains(persistedMerchantResponse, []byte(merchant.Session.RefreshToken)) {
		t.Fatal("merchant refresh secret persisted in idempotency response")
	}
	changed := merchantReq
	changed.BrandName = "Other"
	if _, err = svc.RegisterDemoMerchant(ctx, merchantKey, uuid.NewString(), changed); !errors.Is(err, repository.ErrIdempotencyConflict) {
		t.Fatalf("merchant conflict: %v", err)
	}
	var brandsBefore int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM marcas`).Scan(&brandsBefore); err != nil {
		t.Fatal(err)
	}
	duplicate := merchantReq
	duplicate.BrandName = "Rollback"
	if _, err = svc.RegisterDemoMerchant(ctx, uuid.NewString(), uuid.NewString(), duplicate); !errors.Is(err, repository.ErrEmailExists) {
		t.Fatalf("atomic duplicate: %v", err)
	}
	var brandsAfter, pending int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM marcas`).Scan(&brandsAfter)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM solicitudes_idempotentes WHERE estado='PENDING'`).Scan(&pending)
	if brandsAfter != brandsBefore || pending != 0 {
		t.Fatalf("rollback leaked brand/idempotency: %d/%d pending=%d", brandsBefore, brandsAfter, pending)
	}
	benefit, err := svc.CreateBenefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBenefitRequest{Name: "Beneficio de prueba", Requirement: 5})
	if err != nil || benefit.RequiredStamps == nil || *benefit.RequiredStamps != 5 || benefit.RequiredPoints != nil || benefit.Version != 1 {
		t.Fatalf("create Sellos benefit=%+v err=%v", benefit, err)
	}
	benefits, err := svc.Benefits(ctx, merchant.User.ID, merchant.Merchant.BrandID)
	if err != nil || len(benefits) != 1 || benefits[0].ID != benefit.ID {
		t.Fatalf("list Sellos benefits=%+v err=%v", benefits, err)
	}
	if _, err = svc.CreateBenefit(ctx, customer.ID, merchant.Merchant.BrandID, model.CreateBenefitRequest{Name: "Ajeno", Requirement: 1}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("customer created brand benefit: %v", err)
	}
	if _, err = svc.CreateBenefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBenefitRequest{Name: "Excesivo", Requirement: 10000001}); !errors.Is(err, service.ErrInvalidRequest) {
		t.Fatalf("oversized benefit: %v", err)
	}
	benefitID := benefit.ID
	if benefit.RequiredStamps == nil {
		t.Fatal(err)
	}
	secondBenefit, err := svc.CreateBenefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBenefitRequest{Name: "Segundo beneficio", Requirement: 10})
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := svc.ListBrands(ctx, merchant.User.ID)
	if err != nil || len(contexts) != 1 || len(contexts[0].Benefits) != 2 || contexts[0].Benefits[1].ID != secondBenefit.ID {
		t.Fatalf("multi-benefit contexts=%+v err=%v", contexts, err)
	}
	brandImageSource := image.NewRGBA(image.Rect(0, 0, 8, 6))
	brandImageSource.Set(2, 2, color.RGBA{R: 220, G: 20, B: 60, A: 255})
	var brandPNG bytes.Buffer
	if err = png.Encode(&brandPNG, brandImageSource); err != nil {
		t.Fatal(err)
	}
	firstImage, err := svc.UploadBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, "LOGO", nil, brandPNG.Bytes())
	if err != nil || firstImage.Status != "ACTIVA" || firstImage.URL == "" || firstImage.Width != 8 || firstImage.Height != 6 {
		t.Fatalf("first media=%+v err=%v", firstImage, err)
	}
	if _, err = svc.UploadBrandImage(ctx, customer.ID, merchant.Merchant.BrandID, "ICONO", nil, brandPNG.Bytes()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-tenant media=%v", err)
	}
	secondImage, err := svc.UploadBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, "LOGO", nil, brandPNG.Bytes())
	if err != nil || secondImage.ID == firstImage.ID {
		t.Fatalf("replacement media=%+v err=%v", secondImage, err)
	}
	var oldStatus string
	if err = pool.QueryRow(ctx, `SELECT estado FROM archivos_marca WHERE id=$1`, firstImage.ID).Scan(&oldStatus); err != nil || oldStatus != "DELETE_PENDING" {
		t.Fatalf("old media status=%s err=%v", oldStatus, err)
	}
	images, err := svc.BrandImages(ctx, merchant.User.ID, merchant.Merchant.BrandID)
	if err != nil || len(images) != 1 || images[0].ID != secondImage.ID {
		t.Fatalf("media list=%+v err=%v", images, err)
	}
	if err = svc.DeleteBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, secondImage.ID, secondImage.Version+1); !errors.Is(err, repository.ErrPreconditionFailed) {
		t.Fatalf("media stale delete=%v", err)
	}
	if err = svc.DeleteBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, secondImage.ID, secondImage.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE archivos_marca SET delete_after=now()-interval '1 second' WHERE id=$1`, firstImage.ID); err != nil {
		t.Fatal(err)
	}
	leaseA, leaseB := uuid.NewString(), uuid.NewString()
	dueMedia, err := repo.DueBrandMedia(ctx, 10, leaseA)
	if err != nil || len(dueMedia) != 1 || dueMedia[0].ID != firstImage.ID || dueMedia[0].Status != "DELETE_PENDING" {
		t.Fatalf("media reconciliation claim=%+v err=%v", dueMedia, err)
	}
	if otherClaim, claimErr := repo.DueBrandMedia(ctx, 10, leaseB); claimErr != nil || len(otherClaim) != 0 {
		t.Fatalf("leased media claimed twice=%+v err=%v", otherClaim, claimErr)
	}
	if err = repo.CompleteBrandMediaDeletion(ctx, firstImage.ID, leaseB, ""); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("foreign lease completed media: %v", err)
	}
	if err = repo.CompleteBrandMediaDeletion(ctx, firstImage.ID, leaseA, "temporary object storage failure"); err != nil {
		t.Fatal(err)
	}
	var retryStatus string
	var retryScheduled, leaseReleased bool
	if err = pool.QueryRow(ctx, `SELECT estado,delete_after>now(),lease_owner IS NULL AND lease_until IS NULL FROM archivos_marca WHERE id=$1`, firstImage.ID).Scan(&retryStatus, &retryScheduled, &leaseReleased); err != nil || retryStatus != "DELETE_PENDING" || !retryScheduled || !leaseReleased {
		t.Fatalf("media retry status=%s scheduled=%t lease_released=%t err=%v", retryStatus, retryScheduled, leaseReleased, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE archivos_marca SET delete_after=now()-interval '1 second' WHERE id=$1`, firstImage.ID); err != nil {
		t.Fatal(err)
	}
	dueMedia, err = repo.DueBrandMedia(ctx, 10, leaseB)
	if err != nil || len(dueMedia) != 1 || dueMedia[0].ID != firstImage.ID {
		t.Fatalf("media reconciliation retry=%+v err=%v", dueMedia, err)
	}
	if err = repo.CompleteBrandMediaDeletion(ctx, firstImage.ID, leaseB, ""); err != nil {
		t.Fatal(err)
	}
	var deletedStatus, deletedKey string
	if err = pool.QueryRow(ctx, `SELECT estado,object_key FROM archivos_marca WHERE id=$1`, firstImage.ID).Scan(&deletedStatus, &deletedKey); err != nil || deletedStatus != "DELETED" || deletedKey != "deleted/"+firstImage.ID {
		t.Fatalf("media reconciliation completion status=%s key=%s err=%v", deletedStatus, deletedKey, err)
	}
	media.setSignError(errors.New("presigner unavailable"))
	recoverableImage, err := svc.UploadBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, "ICONO", nil, brandPNG.Bytes())
	media.setSignError(nil)
	if err != nil || recoverableImage.Status != "ACTIVA" || recoverableImage.URL != "" || recoverableImage.URLExpiresAt != nil {
		t.Fatalf("recoverable presign result=%+v err=%v", recoverableImage, err)
	}
	requiredStamps := int64(5)
	merchant.Merchant.Benefit = &model.Benefit{ID: benefitID, ProgramID: merchant.Merchant.Program.ID, Name: "Beneficio de prueba", RequiredStamps: &requiredStamps, Active: true, Version: 1}
	currentMerchant, err := svc.CurrentUser(ctx, merchant.User.ID)
	if err != nil || !currentMerchant.OnboardingComplete {
		t.Fatalf("configured merchant onboarding=%v err=%v", currentMerchant.OnboardingComplete, err)
	}

	memberCode := fmt.Sprintf("#USER-%04d", customer.ID)
	previewReq := model.MovementPreviewRequest{Operation: "ACUMULACION", CustomerCode: memberCode, BranchID: merchant.Merchant.Branch.ID}
	preview, err := svc.Preview(ctx, merchant.User.ID, previewReq)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CardID != 0 || preview.BalanceBefore != 0 || preview.BalanceAfter != 1 {
		t.Fatalf("unexpected preview %+v", preview)
	}
	var cards, movements int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM tarjetas`).Scan(&cards)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM historial_movimientos`).Scan(&movements)
	if cards != 0 || movements != 0 {
		t.Fatalf("preview mutated cards=%d movements=%d", cards, movements)
	}
	confirmReq := model.ConfirmAccumulationRequest{PreviewID: preview.ID, CustomerCode: memberCode, BranchID: merchant.Merchant.Branch.ID}
	normalizedConfirmReq := confirmReq
	normalizedConfirmReq.QRToken = customer.QRToken
	normalizedConfirmReq.CustomerCode = ""
	pendingKey := uuid.NewString()
	pendingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pendingTx.Exec(ctx, `INSERT INTO solicitudes_idempotentes(idempotency_key,actor_scope,operacion,fingerprint,estado) VALUES($1,$2,'CONFIRM_ACUMULACION',$3,'PENDING')`, pendingKey, fmt.Sprintf("user:%d", merchant.User.ID), service.Fingerprint(normalizedConfirmReq)); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConfirmAccumulation(ctx, merchant.User.ID, pendingKey, uuid.NewString(), confirmReq); !errors.Is(err, repository.ErrIdempotencyInProgress) {
		t.Fatalf("in progress: %v", err)
	}
	if err = pendingTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	confirmKey := uuid.NewString()
	var confirmed repository.IdempotentResult
	var wg sync.WaitGroup
	programMovementErrs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, updateErr := svc.UpdateProgram(ctx, merchant.User.ID, merchant.Merchant.BrandID, merchant.Merchant.Program.Version, model.UpdateProgramRequest{Type: "SELLOS", UnitName: "sellos actualizados", Active: true})
		programMovementErrs <- updateErr
	}()
	go func() {
		defer wg.Done()
		var confirmErr error
		confirmed, confirmErr = svc.ConfirmAccumulation(ctx, merchant.User.ID, confirmKey, uuid.NewString(), confirmReq)
		programMovementErrs <- confirmErr
	}()
	wg.Wait()
	close(programMovementErrs)
	for operationErr := range programMovementErrs {
		if operationErr != nil {
			t.Fatalf("program/first movement serialization: %v", operationErr)
		}
	}
	var firstMovementCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM historial_movimientos WHERE marca_id=$1`, merchant.Merchant.BrandID).Scan(&firstMovementCount); err != nil || firstMovementCount != 1 {
		t.Fatalf("first movement count=%d err=%v", firstMovementCount, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE marcas SET nombre='Brand renombrada' WHERE id=$1`, merchant.Merchant.BrandID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sucursales SET nombre='Sucursal renombrada' WHERE id=$1`, merchant.Merchant.Branch.ID); err != nil {
		t.Fatal(err)
	}
	historical, _, err := svc.BrandMovements(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, 20)
	if err != nil || len(historical) != 1 || historical[0].BrandName != "Brand" || historical[0].BranchName != "Main" || historical[0].ProgramIDSnapshot != merchant.Merchant.Program.ID || historical[0].ProgramType != "SELLOS" {
		t.Fatalf("movement snapshots=%+v err=%v", historical, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE marcas SET nombre='Brand' WHERE id=$1`, merchant.Merchant.BrandID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sucursales SET nombre='Main' WHERE id=$1`, merchant.Merchant.Branch.ID); err != nil {
		t.Fatal(err)
	}
	customerCards, cardPage, err := svc.Cards(ctx, customer.ID, 1, 20)
	if err != nil || cardPage.TotalItems != 1 || len(customerCards) != 1 || customerCards[0].Benefit.ID != benefit.ID || customerCards[0].Benefit.Version != 1 {
		t.Fatalf("multi-benefit cards=%+v page=%+v err=%v", customerCards, cardPage, err)
	}
	again, err := svc.ConfirmAccumulation(ctx, merchant.User.ID, confirmKey, uuid.NewString(), confirmReq)
	if err != nil || !again.Replayed || !bytes.Equal(confirmed.Body, again.Body) {
		t.Fatalf("movement replay: %v", err)
	}
	if _, err = svc.ConfirmAccumulation(ctx, merchant.User.ID, uuid.NewString(), uuid.NewString(), confirmReq); !errors.Is(err, repository.ErrPreviewConsumed) {
		t.Fatalf("consumed preview: %v", err)
	}
	altered := confirmReq
	altered.BranchID++
	if _, err = svc.ConfirmAccumulation(ctx, merchant.User.ID, confirmKey, uuid.NewString(), altered); !errors.Is(err, repository.ErrIdempotencyConflict) {
		t.Fatalf("movement conflict: %v", err)
	}

	expired, err := svc.Preview(ctx, merchant.User.ID, previewReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE previews_movimiento SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConfirmAccumulation(ctx, merchant.User.ID, uuid.NewString(), uuid.NewString(), model.ConfirmAccumulationRequest{PreviewID: expired.ID, QRToken: customer.QRToken, BranchID: merchant.Merchant.Branch.ID}); !errors.Is(err, repository.ErrPreviewExpired) {
		t.Fatalf("expired: %v", err)
	}

	for i := 0; i < 4; i++ {
		p, e := svc.Preview(ctx, merchant.User.ID, previewReq)
		if e != nil {
			t.Fatal(e)
		}
		_, e = svc.ConfirmAccumulation(ctx, merchant.User.ID, uuid.NewString(), uuid.NewString(), model.ConfirmAccumulationRequest{PreviewID: p.ID, QRToken: customer.QRToken, BranchID: merchant.Merchant.Branch.ID})
		if e != nil {
			t.Fatal(e)
		}
	}
	redemptionReq := model.MovementPreviewRequest{Operation: "CANJE", QRToken: customer.QRToken, BranchID: merchant.Merchant.Branch.ID, BenefitID: &merchant.Merchant.Benefit.ID}
	redeemPreview, err := svc.Preview(ctx, merchant.User.ID, redemptionReq)
	if err != nil {
		t.Fatal(err)
	}
	redeemed, err := svc.ConfirmRedemption(ctx, merchant.User.ID, uuid.NewString(), uuid.NewString(), model.ConfirmRedemptionRequest{PreviewID: redeemPreview.ID, QRToken: customer.QRToken, BranchID: merchant.Merchant.Branch.ID, BenefitID: merchant.Merchant.Benefit.ID})
	if err != nil {
		t.Fatal(err)
	}
	var redeemedEnvelope web.Envelope[model.Movement]
	if err = json.Unmarshal(redeemed.Body, &redeemedEnvelope); err != nil {
		t.Fatal(err)
	}
	if redeemedEnvelope.Data.BalanceAfter != 0 || redeemedEnvelope.Data.BenefitNameSnapshot == nil || *redeemedEnvelope.Data.BenefitNameSnapshot != "Beneficio de prueba" {
		t.Fatalf("bad redemption %+v", redeemedEnvelope.Data)
	}

	// Restore five stamps, create two previews at the same balance and confirm concurrently.
	for i := 0; i < 5; i++ {
		p, e := svc.Preview(ctx, merchant.User.ID, previewReq)
		if e != nil {
			t.Fatal(e)
		}
		_, e = svc.ConfirmAccumulation(ctx, merchant.User.ID, uuid.NewString(), uuid.NewString(), model.ConfirmAccumulationRequest{PreviewID: p.ID, QRToken: customer.QRToken, BranchID: merchant.Merchant.Branch.ID})
		if e != nil {
			t.Fatal(e)
		}
	}
	p1, _ := svc.Preview(ctx, merchant.User.ID, redemptionReq)
	p2, _ := svc.Preview(ctx, merchant.User.ID, redemptionReq)
	errs := make(chan error, 2)
	for _, p := range []model.Preview{p1, p2} {
		wg.Add(1)
		go func(p model.Preview) {
			defer wg.Done()
			_, e := svc.ConfirmRedemption(ctx, merchant.User.ID, uuid.NewString(), uuid.NewString(), model.ConfirmRedemptionRequest{PreviewID: p.ID, QRToken: customer.QRToken, BranchID: merchant.Merchant.Branch.ID, BenefitID: merchant.Merchant.Benefit.ID})
			errs <- e
		}(p)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for e := range errs {
		if e == nil {
			successes++
		} else if !errors.Is(e, repository.ErrPreviewChanged) && !errors.Is(e, repository.ErrInsufficientBalance) {
			t.Fatalf("unexpected concurrent error: %v", e)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent redemptions accepted=%d", successes)
	}
	var balance int64
	if err = pool.QueryRow(ctx, `SELECT saldo_sellos FROM tarjetas WHERE usuario_id=$1`, customer.ID).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}

	brandCustomers, customerPagination, err := svc.BrandCustomers(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, 20, " CLIENT@ ")
	if err != nil {
		t.Fatal(err)
	}
	if customerPagination.TotalItems != 1 || customerPagination.TotalPages != 1 || len(brandCustomers) != 1 {
		t.Fatalf("brand customer page=%+v items=%+v", customerPagination, brandCustomers)
	}
	brandCustomer := brandCustomers[0]
	if brandCustomer.CustomerID != customer.ID || brandCustomer.Email != "client@example.com" || brandCustomer.Name != "Client" ||
		brandCustomer.BalanceStamps != 0 || brandCustomer.MovementsCount != 12 || brandCustomer.LastMovementAt == nil || brandCustomer.JoinedAt.IsZero() {
		t.Fatalf("unexpected brand customer %+v", brandCustomer)
	}
	customersByID, idPagination, err := svc.BrandCustomers(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, 1, fmt.Sprint(customer.ID))
	if err != nil || len(customersByID) != 1 || idPagination.TotalItems != 1 || customersByID[0].CustomerID != customer.ID {
		t.Fatalf("lookup by customer ID: page=%+v items=%+v err=%v", idPagination, customersByID, err)
	}
	emptyCustomers, emptyPagination, err := svc.BrandCustomers(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, 20, "not-present")
	if err != nil || len(emptyCustomers) != 0 || emptyPagination.TotalItems != 0 || emptyPagination.TotalPages != 0 {
		t.Fatalf("filtered customers page=%+v items=%+v err=%v", emptyPagination, emptyCustomers, err)
	}
	metrics, err := svc.BrandMetrics(ctx, merchant.User.ID, merchant.Merchant.BrandID)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ActiveCustomers != 1 || metrics.CurrentStampBalance != 0 || metrics.Accumulations != 10 || metrics.Redemptions != 2 ||
		metrics.StampsIssued != 10 || metrics.StampsRedeemed != 10 || metrics.LastMovementAt == nil {
		t.Fatalf("unexpected brand metrics %+v", metrics)
	}

	secondReq := model.RegisterDemoMerchantRequest{Email: "owner2@example.com", Password: "merchant-pass", OwnerName: "Owner2", BrandName: "Brand2", BranchName: "Other", ProgramType: "PUNTOS"}
	secondRaw, err := svc.RegisterDemoMerchant(ctx, uuid.NewString(), uuid.NewString(), secondReq)
	if err != nil {
		t.Fatal(err)
	}
	var second web.Envelope[model.DemoMerchantData]
	_ = json.Unmarshal(secondRaw.Body, &second)
	if second.Data.OnboardingComplete || second.Data.Merchant.Program.Type != "PUNTOS" {
		t.Fatalf("unexpected PUNTOS registration %+v", second.Data)
	}
	var firstMembershipID int64
	if err = pool.QueryRow(ctx, `SELECT id FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2`, merchant.User.ID, merchant.Merchant.BrandID).Scan(&firstMembershipID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO membresias_sucursales(membresia_id,sucursal_id,marca_id) VALUES($1,$2,$3)`, firstMembershipID, second.Data.Merchant.Branch.ID, merchant.Merchant.BrandID); err == nil {
		t.Fatal("cross-brand branch membership was accepted")
	}
	pointsBenefit, err := svc.CreateBenefit(ctx, second.Data.User.ID, second.Data.Merchant.BrandID, model.CreateBenefitRequest{Name: "Beneficio Puntos", Requirement: 10000000})
	if err != nil || pointsBenefit.RequiredPoints == nil || *pointsBenefit.RequiredPoints != 10000000 || pointsBenefit.RequiredStamps != nil {
		t.Fatalf("create Puntos benefit=%+v err=%v", pointsBenefit, err)
	}
	pointsPreview := model.MovementPreviewRequest{Operation: "ACUMULACION", QRToken: customer.QRToken, BranchID: second.Data.Merchant.Branch.ID}
	if _, err = svc.Preview(ctx, second.Data.User.ID, pointsPreview); !errors.Is(err, repository.ErrInvalidRequest) {
		t.Fatalf("PUNTOS preview without amount: %v", err)
	}
	tooManyPoints := int64(100001)
	pointsPreview.PointsAmount = &tooManyPoints
	if _, err = svc.Preview(ctx, second.Data.User.ID, pointsPreview); !errors.Is(err, repository.ErrInvalidRequest) {
		t.Fatalf("PUNTOS preview over limit: %v", err)
	}
	manualPoints := int64(100000)
	pointsPreview.PointsAmount = &manualPoints
	pointSnapshot, err := svc.Preview(ctx, second.Data.User.ID, pointsPreview)
	if err != nil || pointSnapshot.Amount != manualPoints || pointSnapshot.ProgramType != "PUNTOS" {
		t.Fatalf("PUNTOS preview=%+v err=%v", pointSnapshot, err)
	}
	pointMovement, err := svc.ConfirmAccumulation(ctx, second.Data.User.ID, uuid.NewString(), uuid.NewString(), model.ConfirmAccumulationRequest{PreviewID: pointSnapshot.ID, QRToken: customer.QRToken, BranchID: second.Data.Merchant.Branch.ID})
	if err != nil {
		t.Fatal(err)
	}
	var pointEnvelope web.Envelope[model.Movement]
	if err = json.Unmarshal(pointMovement.Body, &pointEnvelope); err != nil || pointEnvelope.Data.Amount != manualPoints || pointEnvelope.Data.BalanceAfter != manualPoints || pointEnvelope.Data.ProgramType != "PUNTOS" {
		t.Fatalf("PUNTOS confirmation=%+v err=%v", pointEnvelope, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE programas_fidelidad SET tipo='SELLOS',sellos_por_acumulacion=1 WHERE id=$1`, second.Data.Merchant.Program.ID); err == nil {
		t.Fatal("program type changed after first movement")
	}
	alien := previewReq
	alien.BranchID = second.Data.Merchant.Branch.ID
	if _, err = svc.Preview(ctx, merchant.User.ID, alien); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("ownership: %v", err)
	}
	if _, _, err = svc.BrandCustomers(ctx, second.Data.User.ID, merchant.Merchant.BrandID, 1, 20, ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("customer list ownership: %v", err)
	}
	if _, err = svc.BrandMetrics(ctx, second.Data.User.ID, merchant.Merchant.BrandID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("metrics ownership: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE membresias_marca SET activo=false WHERE usuario_id=$1 AND marca_id=$2`, merchant.User.ID, merchant.Merchant.BrandID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.BrandCustomers(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, 20, ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("inactive membership customer list: %v", err)
	}
	if _, err = svc.BrandMetrics(ctx, merchant.User.ID, merchant.Merchant.BrandID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("inactive membership metrics: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE membresias_marca SET activo=true WHERE usuario_id=$1 AND marca_id=$2`, merchant.User.ID, merchant.Merchant.BrandID); err != nil {
		t.Fatal(err)
	}
	brandNameA, brandNameB := "Marca editada A", "Marca editada B"
	brandEditErrs := make(chan error, 2)
	for _, name := range []*string{&brandNameA, &brandNameB} {
		wg.Add(1)
		go func(name *string) {
			defer wg.Done()
			_, updateErr := svc.UpdateBrand(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, model.UpdateBrandRequest{Name: name})
			brandEditErrs <- updateErr
		}(name)
	}
	wg.Wait()
	close(brandEditErrs)
	brandUpdates := 0
	for updateErr := range brandEditErrs {
		if updateErr == nil {
			brandUpdates++
		} else if !errors.Is(updateErr, repository.ErrPreconditionFailed) {
			t.Fatal(updateErr)
		}
	}
	if brandUpdates != 1 {
		t.Fatalf("concurrent brand updates=%d", brandUpdates)
	}
	commercialBrand, err := svc.Brand(ctx, merchant.User.ID, merchant.Merchant.BrandID)
	if err != nil || commercialBrand.BrandVersion != 2 {
		t.Fatalf("brand edit=%+v err=%v", commercialBrand, err)
	}
	cardTemplate, rewardIcon := "MINIMAL_PRO", "coffee"
	primaryColor, secondaryColor := "#135E4A", "#F4E6C1"
	commercialBrand, err = svc.UpdateBrand(ctx, merchant.User.ID, merchant.Merchant.BrandID, commercialBrand.BrandVersion, model.UpdateBrandRequest{
		PrimaryColor: &primaryColor, SecondaryColor: &secondaryColor, CardTemplate: &cardTemplate, RewardImage: &rewardIcon,
	})
	if err != nil || commercialBrand.BrandVersion != 3 || commercialBrand.CardTemplate == nil || *commercialBrand.CardTemplate != cardTemplate || commercialBrand.RewardImage == nil || *commercialBrand.RewardImage != rewardIcon {
		t.Fatalf("card design brand=%+v err=%v", commercialBrand, err)
	}
	displayLogo, err := svc.UploadBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, "LOGO", nil, brandPNG.Bytes())
	if err != nil || displayLogo.URL == "" {
		t.Fatalf("display logo=%+v err=%v", displayLogo, err)
	}
	benefitArtwork, err := svc.UploadBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, "BENEFICIO", &benefitID, brandPNG.Bytes())
	if err != nil || benefitArtwork.URL == "" {
		t.Fatalf("benefit artwork=%+v err=%v", benefitArtwork, err)
	}
	secondBenefitArtwork, err := svc.UploadBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, "BENEFICIO", &secondBenefit.ID, brandPNG.Bytes())
	if err != nil || secondBenefitArtwork.URL == "" {
		t.Fatalf("second benefit artwork=%+v err=%v", secondBenefitArtwork, err)
	}
	designedCards, _, err := svc.Cards(ctx, customer.ID, 1, 20)
	var designedCard *model.Card
	for i := range designedCards {
		if designedCards[i].BrandID == merchant.Merchant.BrandID {
			designedCard = &designedCards[i]
			break
		}
	}
	if err != nil || designedCard == nil || designedCard.BrandLogo == "" || designedCard.PrimaryColor == nil || *designedCard.PrimaryColor != primaryColor || designedCard.SecondaryColor == nil || *designedCard.SecondaryColor != secondaryColor || designedCard.CardTemplate == nil || *designedCard.CardTemplate != cardTemplate || designedCard.RewardImage == nil || *designedCard.RewardImage != rewardIcon || designedCard.Benefit.ImageURL == "" || len(designedCard.Benefits) != 2 || designedCard.Benefits[0].ImageURL == "" || designedCard.Benefits[1].ImageURL == "" {
		t.Fatalf("customer card branding=%+v err=%v", designedCards, err)
	}
	newBranch, err := svc.CreateBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBranchRequest{Name: "Secundaria"})
	if err != nil || newBranch.Primary {
		t.Fatalf("create branch=%+v err=%v", newBranch, err)
	}
	makePrimary := true
	newBranch, err = svc.UpdateBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, newBranch.ID, newBranch.Version, model.UpdateBranchRequest{Name: "Secundaria", Primary: &makePrimary})
	if err != nil || !newBranch.Primary {
		t.Fatalf("promote branch=%+v err=%v", newBranch, err)
	}
	oldBranch, err := svc.Branch(ctx, merchant.User.ID, merchant.Merchant.BrandID, merchant.Merchant.Branch.ID)
	if err != nil || oldBranch.Primary {
		t.Fatalf("old primary=%+v err=%v", oldBranch, err)
	}
	if err = svc.DeleteBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, oldBranch.ID, oldBranch.Version); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, newBranch.ID, newBranch.Version); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("deleted primary branch: %v", err)
	}
	if _, err = svc.UpdateProgram(ctx, merchant.User.ID, merchant.Merchant.BrandID, commercialBrand.Program.Version, model.UpdateProgramRequest{Type: "PUNTOS", UnitName: "puntos", Active: true}); !errors.Is(err, repository.ErrProgramTypeImmutable) {
		t.Fatalf("program type mutation=%v", err)
	}
	benefitCurrent, err := svc.Benefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, benefit.ID)
	if err != nil {
		t.Fatal(err)
	}
	benefitCurrent, err = svc.ReplaceBenefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, benefit.ID, benefitCurrent.Version, model.ReplaceBenefitRequest{Name: "Premio archivado", Description: "Histórico", Requirement: 5, Active: false})
	if err != nil || benefitCurrent.Active {
		t.Fatalf("deactivate benefit=%+v err=%v", benefitCurrent, err)
	}
	benefitCurrent, err = svc.ReplaceBenefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, benefit.ID, benefitCurrent.Version, model.ReplaceBenefitRequest{Name: "Premio activo", Description: "Restaurado", Requirement: 6, Active: true})
	if err != nil || !benefitCurrent.Active || benefitCurrent.DeletedAt != nil {
		t.Fatalf("reactivate benefit=%+v err=%v", benefitCurrent, err)
	}
	invitationKey := uuid.NewString()
	invitationRequestID := uuid.NewString()
	invitationResult, err := svc.CreateInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, invitationKey, invitationRequestID, model.CreateInvitationRequest{Email: "operator@example.com", Role: "OPERADOR", BranchIDs: []int64{newBranch.ID}})
	var invitationEnvelope web.Envelope[model.BrandInvitation]
	if err == nil {
		err = json.Unmarshal(invitationResult.Body, &invitationEnvelope)
	}
	invitation := invitationEnvelope.Data
	if err != nil || invitation.Status != "PENDIENTE" || len(invitation.BranchIDs) != 1 {
		t.Fatalf("create invitation=%+v err=%v", invitation, err)
	}
	replayedInvitation, err := svc.CreateInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, invitationKey, uuid.NewString(), model.CreateInvitationRequest{Email: "operator@example.com", Role: "OPERADOR", BranchIDs: []int64{newBranch.ID}})
	if err != nil || !replayedInvitation.Replayed || !bytes.Equal(replayedInvitation.Body, invitationResult.Body) {
		t.Fatalf("invitation replay exact=%v err=%v", replayedInvitation.Replayed, err)
	}
	var invitationOutboxCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE invitation_id=$1`, invitation.ID).Scan(&invitationOutboxCount); err != nil || invitationOutboxCount != 1 {
		t.Fatalf("invitation replay outbox count=%d err=%v", invitationOutboxCount, err)
	}
	var inviteOutbox model.OutboxEmail
	if err = pool.QueryRow(ctx, `SELECT id::text,token_ciphertext,token_nonce,token_expires_at FROM email_outbox WHERE tipo='BRAND_INVITATION' AND destinatario='operator@example.com' ORDER BY created_at DESC LIMIT 1`).Scan(&inviteOutbox.ID, &inviteOutbox.Ciphertext, &inviteOutbox.Nonce, &inviteOutbox.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	inviteToken, err := repository.DecryptOutboxToken(inviteOutbox, outboxKey)
	if err != nil || strings.Contains(string(inviteOutbox.Ciphertext), inviteToken) {
		t.Fatalf("invitation outbox token protection err=%v", err)
	}
	publicInvite, err := svc.PublicInvitation(ctx, inviteToken)
	if err != nil || publicInvite.MaskedEmail == "operator@example.com" || publicInvite.Role != "OPERADOR" {
		t.Fatalf("public invitation=%+v err=%v", publicInvite, err)
	}
	type registrationResult struct {
		auth model.AuthData
		err  error
	}
	registrationResults := make(chan registrationResult, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			registered, registerErr := svc.RegisterInvitation(ctx, inviteToken, model.RegisterInvitationRequest{Name: "Operator", Password: "operator-pass"})
			registrationResults <- registrationResult{auth: registered, err: registerErr}
		}()
	}
	wg.Wait()
	close(registrationResults)
	var operatorID int64
	acceptedCount, rejectedCount := 0, 0
	for result := range registrationResults {
		if result.err == nil {
			acceptedCount++
			operatorID = result.auth.User.ID
		} else if errors.Is(result.err, service.ErrIdentityToken) {
			rejectedCount++
		} else {
			t.Fatalf("unexpected double registration error: %v", result.err)
		}
	}
	operatorImages, err := svc.BrandImages(ctx, operatorID, merchant.Merchant.BrandID)
	if err != nil || len(operatorImages) == 0 {
		t.Fatalf("operator read-only media list=%+v err=%v", operatorImages, err)
	}
	operatorCustomers, operatorCustomerPage, err := svc.BrandCustomers(ctx, operatorID, merchant.Merchant.BrandID, 1, 20, "")
	if err != nil || len(operatorCustomers) != 1 || operatorCustomerPage.TotalItems != 1 {
		t.Fatalf("operator customer list page=%+v items=%+v err=%v", operatorCustomerPage, operatorCustomers, err)
	}
	if _, err = svc.UploadBrandImage(ctx, operatorID, merchant.Merchant.BrandID, "LOGO", nil, brandPNG.Bytes()); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("operator uploaded media: %v", err)
	}
	staffMembers, err := svc.Staff(ctx, merchant.User.ID, merchant.Merchant.BrandID)
	if err != nil {
		t.Fatal(err)
	}
	var staff model.StaffMember
	for _, member := range staffMembers {
		if member.UserID == operatorID {
			staff = member
		}
	}
	if acceptedCount != 1 || rejectedCount != 1 || staff.Role != "OPERADOR" || len(staff.BranchIDs) != 1 {
		t.Fatalf("double registration accepted=%d rejected=%d staff=%+v", acceptedCount, rejectedCount, staff)
	}
	if _, err = svc.CreateInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, uuid.NewString(), uuid.NewString(), model.CreateInvitationRequest{Email: "client@example.com", Role: "OPERADOR", BranchIDs: []int64{newBranch.ID}}); !errors.Is(err, repository.ErrInvitationEmailRegistered) {
		t.Fatalf("existing account invitation error=%v", err)
	}
	expiringResult, err := svc.CreateInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, uuid.NewString(), uuid.NewString(), model.CreateInvitationRequest{Email: "newstaff@example.com", Role: "OPERADOR", BranchIDs: []int64{newBranch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var expiringEnvelope web.Envelope[model.BrandInvitation]
	if err = json.Unmarshal(expiringResult.Body, &expiringEnvelope); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE invitaciones_marca SET created_at=now()-interval '4 days',expires_at=now()-interval '1 second' WHERE id=$1`, expiringEnvelope.Data.ID); err != nil {
		t.Fatal(err)
	}
	invitationList, err := svc.Invitations(ctx, merchant.User.ID, merchant.Merchant.BrandID)
	if err != nil {
		t.Fatal(err)
	}
	foundExpired := false
	for _, listed := range invitationList {
		if listed.ID == expiringEnvelope.Data.ID && listed.Status == "EXPIRADA" {
			foundExpired = true
		}
	}
	if !foundExpired {
		t.Fatalf("expired invitation was not materialized: %+v", invitationList)
	}
	replacementResult, err := svc.CreateInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, uuid.NewString(), uuid.NewString(), model.CreateInvitationRequest{Email: "newstaff@example.com", Role: "OPERADOR", BranchIDs: []int64{newBranch.ID}})
	if err != nil {
		t.Fatalf("replacement after expiry: %v", err)
	}
	var replacementEnvelope web.Envelope[model.BrandInvitation]
	if err = json.Unmarshal(replacementResult.Body, &replacementEnvelope); err != nil {
		t.Fatal(err)
	}
	leaseOwner := uuid.NewString()
	var staleOutbox model.OutboxEmail
	if err = pool.QueryRow(ctx, `UPDATE email_outbox SET estado='SENDING',lease_owner=$2,lease_until=now()+interval '2 minutes',intentos=intentos+1 WHERE invitation_id=$1 AND estado='PENDING' RETURNING id::text,destinatario::text,tipo,token_ciphertext,token_nonce,token_expires_at,intentos,lease_owner::text`, replacementEnvelope.Data.ID, leaseOwner).Scan(&staleOutbox.ID, &staleOutbox.To, &staleOutbox.Kind, &staleOutbox.Ciphertext, &staleOutbox.Nonce, &staleOutbox.ExpiresAt, &staleOutbox.Attempts, &staleOutbox.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	staleToken, err := repository.DecryptOutboxToken(staleOutbox, outboxKey)
	if err != nil {
		t.Fatal(err)
	}
	resent, err := svc.ResendInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, replacementEnvelope.Data.ID)
	if err != nil || resent.Version != replacementEnvelope.Data.Version+1 {
		t.Fatalf("resend=%+v err=%v", resent, err)
	}
	if err = repo.ValidateClaimedEmail(ctx, staleOutbox, staleToken); !errors.Is(err, repository.ErrInvitationInvalid) {
		t.Fatalf("stale delivery validation: %v", err)
	}
	if _, err = svc.PublicInvitation(ctx, staleToken); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatalf("stale invitation token: %v", err)
	}
	if err = svc.RevokeInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, replacementEnvelope.Data.ID); err != nil {
		t.Fatalf("revoke invitation: %v", err)
	}
	if err = svc.RevokeInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, replacementEnvelope.Data.ID); !errors.Is(err, repository.ErrInvitationInvalid) {
		t.Fatalf("double revoke: %v", err)
	}
	operatorBranches, err := svc.Branches(ctx, operatorID, merchant.Merchant.BrandID)
	if err != nil || len(operatorBranches) != 1 || operatorBranches[0].ID != newBranch.ID {
		t.Fatalf("operator branch scope=%+v err=%v", operatorBranches, err)
	}
	ownerMovements, ownerPage, err := svc.BrandMovements(ctx, merchant.User.ID, merchant.Merchant.BrandID, 1, 20)
	if err != nil || ownerPage.TotalItems == 0 || len(ownerMovements) == 0 {
		t.Fatalf("owner movement scope=%+v page=%+v err=%v", ownerMovements, ownerPage, err)
	}
	operatorMovements, operatorPage, err := svc.BrandMovements(ctx, operatorID, merchant.Merchant.BrandID, 1, 20)
	if err != nil || operatorPage.TotalItems != 0 || len(operatorMovements) != 0 {
		t.Fatalf("operator saw movements outside assigned branch: %+v page=%+v err=%v", operatorMovements, operatorPage, err)
	}
	racingA, err := svc.CreateBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBranchRequest{Name: "Carrera A"})
	if err != nil {
		t.Fatal(err)
	}
	racingB, err := svc.CreateBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBranchRequest{Name: "Carrera B"})
	if err != nil {
		t.Fatal(err)
	}
	promotionErrs := make(chan error, 2)
	for _, branch := range []model.Branch{racingA, racingB} {
		wg.Add(1)
		go func(branch model.Branch) {
			defer wg.Done()
			primary := true
			_, updateErr := svc.UpdateBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, branch.ID, branch.Version, model.UpdateBranchRequest{Name: branch.Name, Primary: &primary})
			promotionErrs <- updateErr
		}(branch)
	}
	wg.Wait()
	close(promotionErrs)
	for promotionErr := range promotionErrs {
		if promotionErr != nil {
			t.Fatalf("concurrent branch promotion: %v", promotionErr)
		}
	}
	var primaryCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sucursales WHERE marca_id=$1 AND activo AND principal`, merchant.Merchant.BrandID).Scan(&primaryCount); err != nil || primaryCount != 1 {
		t.Fatalf("active primaries=%d err=%v", primaryCount, err)
	}
	ownerContexts, err := svc.ListBrands(ctx, merchant.User.ID)
	if err != nil || len(ownerContexts) != 3 {
		t.Fatalf("owner global branch scope=%d err=%v", len(ownerContexts), err)
	}
	adminRole := "ADMINISTRADOR"
	promotedStaff, err := svc.UpdateStaff(ctx, merchant.User.ID, merchant.Merchant.BrandID, staff.MembershipID, staff.Version, model.UpdateStaffRequest{Role: &adminRole})
	if err != nil || len(promotedStaff.BranchIDs) != 0 {
		t.Fatalf("promote staff=%+v err=%v", promotedStaff, err)
	}
	adminMovements, adminPage, err := svc.BrandMovements(ctx, operatorID, merchant.Merchant.BrandID, 1, 20)
	if err != nil || adminPage.TotalItems != ownerPage.TotalItems || len(adminMovements) == 0 {
		t.Fatalf("admin movement scope=%+v page=%+v err=%v", adminMovements, adminPage, err)
	}
	assignedWhileAdmin := []int64{newBranch.ID}
	if _, err = svc.UpdateStaff(ctx, merchant.User.ID, merchant.Merchant.BrandID, promotedStaff.MembershipID, promotedStaff.Version, model.UpdateStaffRequest{BranchIDs: &assignedWhileAdmin}); !errors.Is(err, repository.ErrInvalidRequest) {
		t.Fatalf("admin accepted branch assignments: %v", err)
	}
	operatorRole := "OPERADOR"
	if _, err = svc.UpdateStaff(ctx, operatorID, merchant.Merchant.BrandID, promotedStaff.MembershipID, promotedStaff.Version, model.UpdateStaffRequest{Role: &operatorRole}); !errors.Is(err, repository.ErrSelfRoleChangeForbidden) {
		t.Fatalf("admin self-demotion: %v", err)
	}
	operatorScope := []int64{newBranch.ID}
	demotedStaff, err := svc.UpdateStaff(ctx, merchant.User.ID, merchant.Merchant.BrandID, promotedStaff.MembershipID, promotedStaff.Version, model.UpdateStaffRequest{Role: &operatorRole, BranchIDs: &operatorScope})
	if err != nil || demotedStaff.Role != "OPERADOR" || len(demotedStaff.BranchIDs) != 1 || demotedStaff.BranchIDs[0] != newBranch.ID {
		t.Fatalf("demote staff with explicit scope=%+v err=%v", demotedStaff, err)
	}
	staffPatchErrs := make(chan error, 2)
	for _, branches := range [][]int64{{newBranch.ID}, {racingA.ID}} {
		assigned := append([]int64(nil), branches...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, patchErr := svc.UpdateStaff(ctx, merchant.User.ID, merchant.Merchant.BrandID, demotedStaff.MembershipID, demotedStaff.Version, model.UpdateStaffRequest{BranchIDs: &assigned})
			staffPatchErrs <- patchErr
		}()
	}
	wg.Wait()
	close(staffPatchErrs)
	staffPatchSuccess, staffPatchStale := 0, 0
	for patchErr := range staffPatchErrs {
		if patchErr == nil {
			staffPatchSuccess++
		} else if errors.Is(patchErr, repository.ErrPreconditionFailed) {
			staffPatchStale++
		} else {
			t.Fatalf("staff concurrent patch: %v", patchErr)
		}
	}
	if staffPatchSuccess != 1 || staffPatchStale != 1 {
		t.Fatalf("staff patch success=%d stale=%d", staffPatchSuccess, staffPatchStale)
	}
	current, err := svc.CurrentUser(ctx, customer.ID)
	if err != nil || current.User.Version != 1 || current.User.AccountType != "CLIENTE_FINAL" {
		t.Fatalf("current account=%+v err=%v", current, err)
	}
	lastNameA, lastNameB := "Primero", "Segundo"
	accountErrs := make(chan error, 2)
	for _, update := range []model.UpdateAccountRequest{
		{Name: model.StringPatch("Cliente A"), LastName: model.StringPatch(lastNameA)},
		{Name: model.StringPatch("Cliente B"), LastName: model.StringPatch(lastNameB)},
	} {
		wg.Add(1)
		go func(update model.UpdateAccountRequest) {
			defer wg.Done()
			_, updateErr := svc.UpdateCurrentUser(ctx, customer.ID, current.User.Version, update)
			accountErrs <- updateErr
		}(update)
	}
	wg.Wait()
	close(accountErrs)
	accountUpdates := 0
	for updateErr := range accountErrs {
		if updateErr == nil {
			accountUpdates++
		} else if !errors.Is(updateErr, repository.ErrPreconditionFailed) {
			t.Fatalf("unexpected account concurrency error: %v", updateErr)
		}
	}
	if accountUpdates != 1 {
		t.Fatalf("concurrent account updates accepted=%d", accountUpdates)
	}
	current, err = svc.CurrentUser(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalName := current.User.Name
	current, err = svc.UpdateCurrentUser(ctx, customer.ID, current.User.Version, model.UpdateAccountRequest{Alias: model.StringPatch("cliente")})
	if err != nil || current.User.Name != originalName || current.User.Alias == nil || *current.User.Alias != "cliente" {
		t.Fatalf("partial account update=%+v err=%v", current, err)
	}
	current, err = svc.UpdateCurrentUser(ctx, customer.ID, current.User.Version, model.UpdateAccountRequest{Alias: model.NullStringPatch(), LastName: model.NullStringPatch()})
	if err != nil || current.User.Name != originalName || current.User.Alias != nil || current.User.LastName != nil || current.User.PhotoURL != nil {
		t.Fatalf("clear account fields=%+v err=%v", current, err)
	}
	photoUpdate, err := svc.UploadProfilePhoto(ctx, customer.ID, current.User.Version, brandPNG.Bytes())
	if err != nil || photoUpdate.Current.User.Version != 5 || photoUpdate.Current.User.PhotoURL == nil || !strings.Contains(photoUpdate.Photo.URL, "/profiles/") {
		t.Fatalf("profile photo update=%+v err=%v", photoUpdate, err)
	}
	current = photoUpdate.Current
	profilePhoto, err := svc.ProfilePhoto(ctx, customer.ID)
	if err != nil || profilePhoto.URL == "" || profilePhoto.MIMEType != "image/png" {
		t.Fatalf("profile photo=%+v err=%v", profilePhoto, err)
	}
	accountExport, err := svc.ExportCurrentUser(ctx, customer.ID)
	if err != nil || len(accountExport.Cards) != 2 || len(accountExport.Movements) != 13 || accountExport.User.Version != 5 {
		t.Fatalf("account export=%+v err=%v", accountExport, err)
	}
	firstActivityPage, firstActivityMeta, err := svc.CustomerMovements(ctx, customer.ID, 1, 5)
	if err != nil || len(firstActivityPage) != 5 || firstActivityMeta.TotalItems != 13 || firstActivityMeta.TotalPages != 3 {
		t.Fatalf("customer activity page=%+v meta=%+v err=%v", firstActivityPage, firstActivityMeta, err)
	}
	lastActivityPage, lastActivityMeta, err := svc.CustomerMovements(ctx, customer.ID, 3, 5)
	if err != nil || len(lastActivityPage) != 3 || lastActivityMeta.TotalItems != 13 || lastActivityPage[0].ID == firstActivityPage[0].ID {
		t.Fatalf("customer last activity page=%+v meta=%+v err=%v", lastActivityPage, lastActivityMeta, err)
	}
	for _, benefitID := range []int64{benefit.ID, secondBenefit.ID} {
		currentBenefit, benefitErr := svc.Benefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, benefitID)
		if benefitErr != nil {
			t.Fatal(benefitErr)
		}
		if currentBenefit.RequiredStamps == nil {
			t.Fatal("expected a stamps benefit")
		}
		_, benefitErr = svc.ReplaceBenefit(ctx, merchant.User.ID, merchant.Merchant.BrandID, benefitID, currentBenefit.Version, model.ReplaceBenefitRequest{Name: currentBenefit.Name, Description: currentBenefit.Description, Requirement: *currentBenefit.RequiredStamps, Active: false})
		if benefitErr != nil {
			t.Fatal(benefitErr)
		}
	}
	cardsWithoutBenefits, _, err := svc.Cards(ctx, customer.ID, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	var cardStillVisible bool
	for _, card := range cardsWithoutBenefits {
		if card.BrandID == merchant.Merchant.BrandID {
			cardStillVisible = true
			if card.Benefit != nil || len(card.Benefits) != 0 {
				t.Fatalf("inactive rewards leaked into card: %+v", card)
			}
		}
	}
	if !cardStillVisible {
		t.Fatal("card disappeared after its last benefit was deactivated")
	}
	if _, err = repo.AnonymizeAccount(ctx, merchant.User.ID, merchant.User.Version); !errors.Is(err, repository.ErrOwnershipTransfer) {
		t.Fatalf("last owner deletion: %v", err)
	}
	deleteLogin, err := svc.Login(ctx, model.LoginRequest{Email: "client@example.com", Password: "customer-pass"})
	if err != nil {
		t.Fatal(err)
	}
	if err = identitySvc.RequestPasswordReset(ctx, model.EmailRequest{Email: "client@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO solicitudes_idempotentes(idempotency_key,actor_scope,operacion,fingerprint,estado,response_status,response_body) VALUES($1,$2,'ACCOUNT_TEST',decode(repeat('03',32),'hex'),'COMPLETED',200,$3)`, uuid.NewString(), fmt.Sprintf("user:%d", customer.ID), []byte(`{"email":"client@example.com","name":"Client"}`)); err != nil {
		t.Fatal(err)
	}
	var ledgerBefore int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM historial_movimientos h JOIN tarjetas t ON t.id=h.tarjeta_id WHERE t.usuario_id=$1`, customer.ID).Scan(&ledgerBefore); err != nil {
		t.Fatal(err)
	}
	deletedAt, err := repo.AnonymizeAccount(ctx, customer.ID, accountExport.User.Version)
	if err != nil || deletedAt.IsZero() {
		t.Fatalf("anonymize customer: %v", err)
	}
	if w = authorizedRequest(deleteLogin.Session.AccessToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted account access status=%d", w.Code)
	}
	var tombstone, anonymizedName string
	var inactive, credentialsCleared, qrCleared, profileCleared bool
	var ledgerAfter int
	if err = pool.QueryRow(ctx, `SELECT email::text,nombre,NOT activo,password_hash IS NULL AND google_id IS NULL AND email_verified_at IS NULL,qr_hash IS NULL,apellido IS NULL AND alias IS NULL AND foto_url IS NULL FROM usuarios WHERE id=$1`, customer.ID).Scan(&tombstone, &anonymizedName, &inactive, &credentialsCleared, &qrCleared, &profileCleared); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM historial_movimientos h JOIN tarjetas t ON t.id=h.tarjeta_id WHERE t.usuario_id=$1`, customer.ID).Scan(&ledgerAfter); err != nil {
		t.Fatal(err)
	}
	if tombstone != fmt.Sprintf("deleted-%d@anon.invalid", customer.ID) || anonymizedName != "Cuenta anonimizada" || !inactive || !credentialsCleared || !qrCleared || !profileCleared || ledgerAfter != ledgerBefore {
		t.Fatalf("anonymized state email=%s name=%s inactive=%t credentials=%t qr=%t profile=%t ledger=%d/%d", tombstone, anonymizedName, inactive, credentialsCleared, qrCleared, profileCleared, ledgerAfter, ledgerBefore)
	}
	var piiLeaks int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM email_outbox WHERE destinatario::text ILIKE '%client@example.com%' OR COALESCE(cuerpo_texto,'') ILIKE '%Client%' OR COALESCE(cuerpo_html,'') ILIKE '%client@example.com%')+(SELECT count(*) FROM solicitudes_idempotentes WHERE actor_scope=$1 OR convert_from(COALESCE(response_body,''::bytea),'UTF8') ILIKE '%client@example.com%')+(SELECT count(*) FROM invitaciones_marca WHERE email::text ILIKE '%client@example.com%')`, fmt.Sprintf("user:%d", customer.ID)).Scan(&piiLeaks); err != nil || piiLeaks != 0 {
		t.Fatalf("PII leaks after anonymization=%d err=%v", piiLeaks, err)
	}
	pendingMerchantRaw, err := identitySvc.RegisterDemoMerchant(ctx, uuid.NewString(), uuid.NewString(), model.RegisterDemoMerchantRequest{Email: "pendingmerchant@example.com", Password: "pending-merchant-pass", OwnerName: "Pending Owner", BrandName: "Pending Brand", BranchName: "Principal", ProgramType: "SELLOS"})
	if err != nil {
		t.Fatal(err)
	}
	var pendingMerchant web.Envelope[model.DemoMerchantData]
	if err = json.Unmarshal(pendingMerchantRaw.Body, &pendingMerchant); err != nil {
		t.Fatal(err)
	}
	if pendingMerchant.Data.Session != nil || !pendingMerchant.Data.VerificationRequired || pendingMerchant.Data.Merchant.BrandID < 1 {
		t.Fatalf("pending merchant=%+v", pendingMerchant.Data)
	}
	var pendingMerchantSessions int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sesiones_auth WHERE usuario_id=$1`, pendingMerchant.Data.User.ID).Scan(&pendingMerchantSessions); err != nil || pendingMerchantSessions != 0 {
		t.Fatalf("pending merchant sessions=%d err=%v", pendingMerchantSessions, err)
	}
	claimedEmails, err := repo.ClaimEmails(ctx, 20)
	if err != nil || len(claimedEmails) < 2 {
		t.Fatalf("claimed outbox=%+v err=%v", claimedEmails, err)
	}
	if err = repo.MarkEmailSent(ctx, claimedEmails[0].ID, uuid.NewString()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("stale lease owner updated email: %v", err)
	}
	if err = repo.MarkEmailSent(ctx, claimedEmails[0].ID, claimedEmails[0].LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if err = repo.MarkEmailFailed(ctx, claimedEmails[1].ID, claimedEmails[1].LeaseOwner, claimedEmails[1].Attempts, errors.New("temporary smtp failure")); err != nil {
		t.Fatal(err)
	}
	retentionNow := time.Now().UTC()
	oldIdempotency := uuid.NewString()
	recentIdempotency := uuid.NewString()
	oldSession := uuid.NewString()
	oldToken := uuid.NewString()
	redactEmail := uuid.NewString()
	deleteEmail := uuid.NewString()
	retentionTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retentionStatements := []struct {
		query string
		args  []any
	}{
		{`UPDATE previews_movimiento SET expires_at=$1 WHERE id=(SELECT id FROM previews_movimiento ORDER BY created_at LIMIT 1)`, []any{retentionNow.Add(-8 * 24 * time.Hour)}},
		{`INSERT INTO solicitudes_idempotentes(idempotency_key,actor_scope,operacion,fingerprint,estado,response_status,response_body,completed_at,created_at) VALUES($1,'retention:test','TEST',decode(repeat('11',32),'hex'),'COMPLETED',200,'{}',$2,$2),($3,'retention:test','TEST',decode(repeat('12',32),'hex'),'COMPLETED',200,'{}',$4,$4)`, []any{oldIdempotency, retentionNow.Add(-31 * 24 * time.Hour), recentIdempotency, retentionNow}},
		{`INSERT INTO sesiones_auth(id,usuario_id,refresh_hash,expires_at,revoked_at,created_at,family_id,auth_time) VALUES($1,$2,decode(repeat('13',32),'hex'),$3,$3,$4,$1,$4)`, []any{oldSession, customer.ID, retentionNow.Add(-31 * 24 * time.Hour), retentionNow.Add(-32 * 24 * time.Hour)}},
		{`INSERT INTO tokens_identidad_email(id,usuario_id,proposito,token_hash,expires_at,consumed_at,created_at) VALUES($1,$2,'RESET_PASSWORD',decode(repeat('14',32),'hex'),$3,$3,$4)`, []any{oldToken, customer.ID, retentionNow.Add(-31 * 24 * time.Hour), retentionNow.Add(-32 * 24 * time.Hour)}},
		{`INSERT INTO email_outbox(id,usuario_id,tipo,destinatario,asunto,estado,intentos,disponible_at,ultimo_error,created_at) VALUES($1,$2,'RESET_PASSWORD','redact@example.com','old','FAILED',1,$3,'secret error',$3),($4,$2,'RESET_PASSWORD','delete@example.com','old','FAILED',1,$5,'secret error',$5)`, []any{redactEmail, customer.ID, retentionNow.Add(-8 * 24 * time.Hour), deleteEmail, retentionNow.Add(-31 * 24 * time.Hour)}},
	}
	for _, fixture := range retentionStatements {
		if _, err = retentionTx.Exec(ctx, fixture.query, fixture.args...); err != nil {
			_ = retentionTx.Rollback(ctx)
			t.Fatalf("retention fixtures: %v", err)
		}
	}
	if err = retentionTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := repo.ApplyRetention(ctx, repository.RetentionPolicy{Now: retentionNow, BatchSize: 100, PreviewRetention: 7 * 24 * time.Hour, IdempotencyRetention: 30 * 24 * time.Hour, SessionRetention: 30 * 24 * time.Hour, IdentityRetention: 7 * 24 * time.Hour, OutboxRedactAfter: 7 * 24 * time.Hour, OutboxRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if stats.PreviewsDeleted < 1 || stats.IdempotenciesDeleted != 1 || stats.SessionsDeleted != 1 || stats.IdentityTokensDeleted != 1 || stats.OutboxRedacted < 1 || stats.OutboxDeleted != 1 {
		t.Fatalf("retention stats=%+v", stats)
	}
	var recentExists, redacted bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM solicitudes_idempotentes WHERE idempotency_key=$1),EXISTS(SELECT 1 FROM email_outbox WHERE id=$2 AND redacted_at IS NOT NULL AND destinatario::text LIKE 'redacted-%@anon.invalid' AND ultimo_error IS NULL)`, recentIdempotency, redactEmail).Scan(&recentExists, &redacted); err != nil || !recentExists || !redacted {
		t.Fatalf("retention preservation recent=%t redacted=%t err=%v", recentExists, redacted, err)
	}
	retiredInvitationResult, err := svc.CreateInvitation(ctx, merchant.User.ID, merchant.Merchant.BrandID, uuid.NewString(), uuid.NewString(), model.CreateInvitationRequest{Email: "retired-brand-staff@example.com", Role: "OPERADOR", BranchIDs: []int64{newBranch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var retiredInvitationEnvelope web.Envelope[model.BrandInvitation]
	if err = json.Unmarshal(retiredInvitationResult.Body, &retiredInvitationEnvelope); err != nil {
		t.Fatal(err)
	}
	var retiredInvitationOutbox model.OutboxEmail
	if err = pool.QueryRow(ctx, `SELECT id::text,token_ciphertext,token_nonce FROM email_outbox WHERE invitation_id=$1`, retiredInvitationEnvelope.Data.ID).Scan(&retiredInvitationOutbox.ID, &retiredInvitationOutbox.Ciphertext, &retiredInvitationOutbox.Nonce); err != nil {
		t.Fatal(err)
	}
	retiredInvitationToken, err := repository.DecryptOutboxToken(retiredInvitationOutbox, outboxKey)
	if err != nil {
		t.Fatal(err)
	}
	pendingImageID := uuid.NewString()
	pendingImage, err := repo.ReserveBrandImage(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.BrandImage{
		ID: pendingImageID, BrandID: merchant.Merchant.BrandID, Type: "LOGO", ObjectKey: fmt.Sprintf("brands/%d/%s.png", merchant.Merchant.BrandID, pendingImageID), MIMEType: "image/png", ByteSize: int64(brandPNG.Len()), Width: 8, Height: 6,
	}, make([]byte, 32))
	if err != nil || pendingImage.Status != "UPLOAD_PENDING" {
		t.Fatalf("reserve pending media=%+v err=%v", pendingImage, err)
	}
	var brandVersion int
	if err = pool.QueryRow(ctx, `SELECT version FROM marcas WHERE id=$1`, merchant.Merchant.BrandID).Scan(&brandVersion); err != nil {
		t.Fatal(err)
	}
	type checkoutReservation struct {
		record repository.SubscriptionRecord
		err    error
	}
	reservations := make(chan checkoutReservation, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, record, reserveErr := repo.ReserveSubscriptionCheckout(ctx, merchant.User.ID, merchant.Merchant.BrandID,
				fmt.Sprintf("puntazo:brand:%d:%s", merchant.Merchant.BrandID, uuid.NewString()), 12300, 45600)
			reservations <- checkoutReservation{record, reserveErr}
		}()
	}
	wg.Wait()
	close(reservations)
	var reservedReference string
	for result := range reservations {
		if result.err != nil || result.record.Subscription.Status != "CREATING" || result.record.TrialMonths != 1 || result.record.Subscription.ActiveBranches < 1 || result.record.Subscription.MonthlyAmountCents != 12300*result.record.Subscription.ActiveBranches {
			t.Fatalf("checkout reservation=%+v err=%v", result.record, result.err)
		}
		if reservedReference == "" {
			reservedReference = result.record.ExternalReference
		}
		if result.record.ExternalReference != reservedReference {
			t.Fatalf("concurrent checkouts used different provider keys")
		}
	}
	claimed, claimErr := repo.ClaimSubscriptionProviderCall(ctx, merchant.Merchant.BrandID, reservedReference)
	if claimErr != nil || !claimed {
		t.Fatalf("first provider claim=%t err=%v", claimed, claimErr)
	}
	claimed, claimErr = repo.ClaimSubscriptionProviderCall(ctx, merchant.Merchant.BrandID, reservedReference)
	if claimErr != nil || claimed {
		t.Fatalf("duplicate provider call allowed=%t err=%v", claimed, claimErr)
	}
	billingProbe := &fakeBillingProvider{}
	svc.Billing = billingProbe
	if _, err = svc.CreateSubscriptionCheckout(ctx, merchant.User.ID, merchant.Merchant.BrandID, uuid.NewString()); !errors.Is(err, service.ErrBillingInProgress) || billingProbe.createCalls != 0 {
		t.Fatalf("uncertain checkout retried provider: calls=%d err=%v", billingProbe.createCalls, err)
	}
	if _, err = svc.CreateBranch(ctx, merchant.User.ID, merchant.Merchant.BrandID, model.CreateBranchRequest{Name: "No debe crearse"}); !errors.Is(err, repository.ErrSubscriptionChangeRequired) {
		t.Fatalf("branch changed during checkout: %v", err)
	}
	if err = svc.DeleteBrand(ctx, merchant.User.ID, merchant.Merchant.BrandID, brandVersion); !errors.Is(err, repository.ErrSubscriptionChangeRequired) {
		t.Fatalf("brand deleted during checkout: %v", err)
	}
	provider := model.BillingSubscriptionResult{ID: "provider-reservation-1", Status: "pending", ExternalReference: reservedReference, CheckoutURL: "https://mp.example/checkout"}
	if _, err = repo.SaveSubscriptionCheckout(ctx, merchant.Merchant.BrandID, provider); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SaveSubscriptionCheckout(ctx, merchant.Merchant.BrandID, provider); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("completed checkout overwritten: %v", err)
	}
	provider.Status = "cancelled"
	if _, err = repo.UpdateSubscriptionFromProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	secondReference := fmt.Sprintf("puntazo:brand:%d:%s", merchant.Merchant.BrandID, uuid.NewString())
	_, secondReservation, err := repo.ReserveSubscriptionCheckout(ctx, merchant.User.ID, merchant.Merchant.BrandID, secondReference, 12300, 45600)
	if err != nil || secondReservation.TrialMonths != 0 || secondReservation.Subscription.Status != "CREATING" {
		t.Fatalf("second checkout=%+v err=%v", secondReservation, err)
	}
	claimed, claimErr = repo.ClaimSubscriptionProviderCall(ctx, merchant.Merchant.BrandID, secondReference)
	if claimErr != nil || !claimed {
		t.Fatalf("second provider claim=%t err=%v", claimed, claimErr)
	}
	provider = model.BillingSubscriptionResult{ID: "provider-reservation-2", Status: "pending", ExternalReference: secondReference, CheckoutURL: "https://mp.example/recovered"}
	if err = repo.RecordSubscriptionWebhook(ctx, "recovery-notification-1", "subscription_preapproval", provider); err != nil {
		t.Fatalf("webhook could not reconcile uncertain checkout: %v", err)
	}
	recovered, err := repo.GetSubscriptionRecord(ctx, merchant.Merchant.BrandID)
	if err != nil || recovered.ProviderID != provider.ID || recovered.Subscription.Status != "PENDING" {
		t.Fatalf("webhook recovery=%+v err=%v", recovered, err)
	}
	provider.Status = "cancelled"
	if _, err = repo.UpdateSubscriptionFromProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteBrand(ctx, merchant.User.ID, merchant.Merchant.BrandID, brandVersion); err != nil {
		t.Fatal(err)
	}
	var scheduledByBrandDelete int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM archivos_marca WHERE id=ANY($1::uuid[]) AND estado='DELETE_PENDING' AND delete_after>=now()+interval '23 hours'`, []string{recoverableImage.ID, pendingImage.ID}).Scan(&scheduledByBrandDelete); err != nil || scheduledByBrandDelete != 2 {
		t.Fatalf("brand media scheduled=%d err=%v", scheduledByBrandDelete, err)
	}
	if _, err = repo.ActivateBrandImage(ctx, merchant.User.ID, pendingImage.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("media activated after brand deletion: %v", err)
	}
	var retiredInvitationStatus, retiredOutboxStatus string
	var retiredTokenScrubbed bool
	if err = pool.QueryRow(ctx, `SELECT i.estado,o.estado,o.token_ciphertext IS NULL AND o.token_nonce IS NULL AND o.token_expires_at IS NULL FROM invitaciones_marca i JOIN email_outbox o ON o.invitation_id=i.id WHERE i.id=$1`, retiredInvitationEnvelope.Data.ID).Scan(&retiredInvitationStatus, &retiredOutboxStatus, &retiredTokenScrubbed); err != nil || retiredInvitationStatus != "REVOCADA" || retiredOutboxStatus != "FAILED" || !retiredTokenScrubbed {
		t.Fatalf("deleted brand invitation=%s outbox=%s scrubbed=%t err=%v", retiredInvitationStatus, retiredOutboxStatus, retiredTokenScrubbed, err)
	}
	if _, err = svc.RegisterInvitation(ctx, retiredInvitationToken, model.RegisterInvitationRequest{Name: "Retired Brand Staff", Password: "retired-brand-pass"}); !errors.Is(err, service.ErrIdentityToken) {
		t.Fatalf("registered invitation from deleted brand: %v", err)
	}
	if err = repo.CheckSchema(ctx, "0021"); err != nil {
		t.Fatal(err)
	}
	if err = repo.CheckSchema(ctx, "9999"); err == nil {
		t.Fatal("readiness accepted wrong schema")
	}
	t.Logf("verified brand=%d customer=%d movements persisted", merchant.Merchant.BrandID, customer.ID)
}

type fakeMediaStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	signErr error
}

type fakeBillingProvider struct{ createCalls int }

func (f *fakeBillingProvider) CreateSubscription(context.Context, model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error) {
	f.createCalls++
	return model.BillingSubscriptionResult{}, errors.New("unexpected provider create")
}
func (*fakeBillingProvider) GetSubscription(context.Context, string) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{}, errors.New("unexpected provider lookup")
}
func (*fakeBillingProvider) CancelSubscription(context.Context, string, string) (model.BillingSubscriptionResult, error) {
	return model.BillingSubscriptionResult{}, errors.New("unexpected provider cancellation")
}

func (f *fakeMediaStore) Put(_ context.Context, key, _ string, body, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = append([]byte(nil), body...)
	return nil
}
func (f *fakeMediaStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}
func (f *fakeMediaStore) SignedGet(_ context.Context, key string, _ time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.signErr != nil {
		return "", f.signErr
	}
	return "https://private.example.test/" + key + "?signature=test", nil
}
func (f *fakeMediaStore) ReadEmailImage(_ context.Context, key string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.objects[key]...), "image/png", nil
}
func (f *fakeMediaStore) Ready(context.Context) error { return nil }
func (f *fakeMediaStore) setSignError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signErr = err
}

func authorizedRequestFor(tokens *auth.Tokens, repo *repository.Repository, accessToken string) *httptest.ResponseRecorder {
	router := gin.New()
	router.GET("/protected", middleware.RequireAuth(tokens, repo), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

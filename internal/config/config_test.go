package config

import (
	"encoding/base64"
	"testing"
)

func TestFourDigits(t *testing.T) {
	for _, value := range []string{"0001", "9999"} {
		if !fourDigits(value) {
			t.Fatalf("valid schema version rejected: %s", value)
		}
	}
	for _, value := range []string{"001", "00001", "00a1", "abcd"} {
		if fourDigits(value) {
			t.Fatalf("invalid schema version accepted: %s", value)
		}
	}
}

func TestJWTIssuerDefaultsToPuntazo(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("JWT_ISSUER", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTIssuer != "puntazo" {
		t.Fatalf("JWTIssuer = %q, want puntazo", cfg.JWTIssuer)
	}
}

func TestJWTIssuerCanBeConfigured(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("JWT_ISSUER", "puntazo-staging")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTIssuer != "puntazo-staging" {
		t.Fatalf("JWTIssuer = %q, want puntazo-staging", cfg.JWTIssuer)
	}
}

func TestS3PublicEndpointDefaultsToInternalEndpoint(t *testing.T) {
	setRequiredEnv(t)
	setS3Env(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3PublicEndpoint != cfg.S3Endpoint {
		t.Fatalf("S3PublicEndpoint = %q, want internal endpoint %q", cfg.S3PublicEndpoint, cfg.S3Endpoint)
	}
}

func TestS3PublicEndpointCanDifferFromInternalEndpoint(t *testing.T) {
	setRequiredEnv(t)
	setS3Env(t)
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://media.puntazo.test")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3PublicEndpoint != "https://media.puntazo.test" {
		t.Fatalf("S3PublicEndpoint = %q", cfg.S3PublicEndpoint)
	}
}

func TestS3PublicEndpointRejectsUncleanURLs(t *testing.T) {
	for _, endpoint := range []string{
		"minio.puntazo.test",
		"https://user:password@media.puntazo.test",
		"https://media.puntazo.test?token=secret",
		"https://media.puntazo.test#fragment",
	} {
		t.Run(endpoint, func(t *testing.T) {
			setRequiredEnv(t)
			setS3Env(t)
			t.Setenv("S3_PUBLIC_ENDPOINT", endpoint)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted invalid S3_PUBLIC_ENDPOINT %q", endpoint)
			}
		})
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgresql://puntazo:puntazo@localhost:5432/puntazo")
	t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
	t.Setenv("QR_PEPPER", "abcdefghijklmnopqrstuvwxyzABCDEF")
	t.Setenv("APP_ENV", "development")
	t.Setenv("EMAIL_VERIFICATION_REQUIRED", "false")
	t.Setenv("MAIL_PROVIDER", "disabled")
}

func TestDevelopmentMailCaptureRequiresEncryptionAndRejectsProduction(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("OUTBOX_ENCRYPTION_KEY", "")
	t.Setenv("MAIL_PROVIDER", "capture")
	t.Setenv("MAIL_CAPTURE_DIRECTORY", t.TempDir())
	t.Setenv("MAIL_FROM_ADDRESS", "no-reply@puntazo.test")
	if _, err := Load(); err == nil {
		t.Fatal("capture accepted missing cipher key")
	}
	t.Setenv("OUTBOX_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_CAPTURE_DIRECTORY", "relative-directory")
	if _, err := Load(); err == nil {
		t.Fatal("capture accepted relative directory")
	}
	t.Setenv("MAIL_CAPTURE_DIRECTORY", t.TempDir())
	t.Setenv("APP_ENV", "production")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted capture")
	}
}

func setS3Env(t *testing.T) {
	t.Helper()
	t.Setenv("MEDIA_PROVIDER", "s3")
	t.Setenv("S3_ENDPOINT", "http://minio.internal.test:9000")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_BUCKET", "puntazo-media")
	t.Setenv("S3_ACCESS_KEY_ID", "test-access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test-secret")
}

func TestProductionRequiresVerifiedSMTPIdentity(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_ENV", "production")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted verification disabled")
	}
	t.Setenv("EMAIL_VERIFICATION_REQUIRED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("verification accepted disabled mail")
	}
	t.Setenv("MAIL_PROVIDER", "smtp")
	t.Setenv("MAIL_FROM_ADDRESS", "no-reply@puntazo.test")
	t.Setenv("SMTP_HOST", "smtp.puntazo.test")
	t.Setenv("SMTP_TLS_MODE", "starttls")
	t.Setenv("OUTBOX_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("separate-outbox-key-32-bytes!!!!")))
	t.Setenv("PUBLIC_APP_URL", "https://app.puntazo.test")
	t.Setenv("MEDIA_PROVIDER", "s3")
	t.Setenv("S3_ENDPOINT", "https://s3.puntazo.test")
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://media.puntazo.test")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("S3_BUCKET", "puntazo-media")
	t.Setenv("S3_ACCESS_KEY_ID", "test-access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("RATE_LIMIT_PROVIDER", "redis")
	t.Setenv("REDIS_URL", "rediss://redis.puntazo.test:6379/0")
	t.Setenv("RATE_LIMIT_PREFIX", "puntazo:production")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("S3_PUBLIC_ENDPOINT", "http://media.puntazo.test")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted an insecure S3_PUBLIC_ENDPOINT")
	}
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://media.puntazo.test")
	t.Setenv("S3_SERVER_SIDE_ENCRYPTION", "DISABLED")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted media storage without AES256 server-side encryption")
	}
	t.Setenv("S3_SERVER_SIDE_ENCRYPTION", "AES256")
	t.Setenv("PUBLIC_APP_URL", "http://app.puntazo.test")
	if _, err := Load(); err == nil {
		t.Fatal("production accepted an insecure PUBLIC_APP_URL")
	}
}

func TestBranchSimulatorIsolationGate(t *testing.T) {
	for _, tt := range []struct {
		name, app, provider, token, env string
		want                            bool
	}{
		{"local", "http://localhost:4175", "disabled", "", "development", true},
		{"public", "https://testing.puntazo.pro", "disabled", "", "development", false},
		{"real provider", "http://localhost:4175", "api", "token", "development", false},
		{"stray token", "http://localhost:4175", "disabled", "token", "development", false},
		{"production", "https://puntazo.pro", "disabled", "", "production", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("BRANCH_PRORATION_ENABLED", "true")
			t.Setenv("BRANCH_PAYMENT_SIMULATOR", "true")
			t.Setenv("PUBLIC_APP_URL", tt.app)
			t.Setenv("MERCADO_PAGO_PROVIDER", tt.provider)
			t.Setenv("MERCADO_PAGO_ACCESS_TOKEN", tt.token)
			t.Setenv("APP_ENV", tt.env)
			_, e := Load()
			if (e == nil) != tt.want {
				t.Fatalf("gate error=%v want allowed%v", e, tt.want)
			}
		})
	}
}

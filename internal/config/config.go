package config

import (
	"encoding/base64"
	"errors"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const MaxJSONBytes int64 = 1 << 20

type Config struct {
	DatabaseURL               string
	JWTSecret                 string
	JWTIssuer                 string
	QRPepper                  string
	DemoSignupEnabled         bool
	EmailVerificationRequired bool
	PublicAppURL              string
	MailProvider              string
	MailFromAddress           string
	MailFromName              string
	SMTPHost                  string
	SMTPPort                  int
	SMTPUsername              string
	SMTPPassword              string
	SMTPTLSMode               string
	MailPollInterval          time.Duration
	MediaProvider             string
	S3Endpoint                string
	S3PublicEndpoint          string
	S3Region                  string
	S3Bucket                  string
	S3AccessKeyID             string
	S3SecretAccessKey         string
	S3ServerSideEncryption    string
	MediaURLTTL               time.Duration
	MediaCleanupInterval      time.Duration
	MediaUploadGlobalLimit    int
	MediaUploadActorLimit     int
	RetentionInterval         time.Duration
	RetentionBatchSize        int
	PreviewRetention          time.Duration
	IdempotencyRetention      time.Duration
	SessionRetention          time.Duration
	IdentityTokenRetention    time.Duration
	OutboxRedactAfter         time.Duration
	OutboxRetention           time.Duration
	OutboxEncryptionKey       []byte
	AppVersion                string
	GitCommit                 string
	ExpectedSchemaVersion     string
	Port                      string
	TrustedProxyCount         int
	ReadTimeout               time.Duration
	WriteTimeout              time.Duration
	IdleTimeout               time.Duration
	Production                bool
	RateLimitProvider         string
	RedisURL                  string
	RateLimitPrefix           string
	RateLimitTimeout          time.Duration
	RateLimitDevFallback      bool
	MercadoPagoProvider       string
	MercadoPagoAccessToken    string
	MercadoPagoWebhookSecret  string
	MercadoPagoAPIURL         string
	MercadoPagoBranchPrice    int64
	MercadoPagoPointsPrice    int64
	MercadoPagoTimeout        time.Duration
}

func Load() (Config, error) {
	s3Endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), JWTSecret: os.Getenv("JWT_SECRET"), JWTIssuer: envDefault("JWT_ISSUER", "puntazo"),
		QRPepper:          os.Getenv("QR_PEPPER"),
		DemoSignupEnabled: envBool("DEMO_SIGNUP_ENABLED", false), AppVersion: envDefault("APP_VERSION", "dev"),
		EmailVerificationRequired: envBool("EMAIL_VERIFICATION_REQUIRED", false), PublicAppURL: envDefault("PUBLIC_APP_URL", "http://localhost:8081"),
		MailProvider: strings.ToLower(envDefault("MAIL_PROVIDER", "disabled")), MailFromAddress: strings.TrimSpace(os.Getenv("MAIL_FROM_ADDRESS")), MailFromName: envDefault("MAIL_FROM_NAME", "Puntazo"),
		SMTPHost: strings.TrimSpace(os.Getenv("SMTP_HOST")), SMTPPort: envInt("SMTP_PORT", 587), SMTPUsername: os.Getenv("SMTP_USERNAME"), SMTPPassword: os.Getenv("SMTP_PASSWORD"), SMTPTLSMode: strings.ToLower(envDefault("SMTP_TLS_MODE", "starttls")), MailPollInterval: time.Duration(envInt("MAIL_POLL_INTERVAL_SECONDS", 5)) * time.Second,
		MediaProvider: strings.ToLower(envDefault("MEDIA_PROVIDER", "disabled")), S3Endpoint: s3Endpoint, S3PublicEndpoint: strings.TrimSpace(envDefault("S3_PUBLIC_ENDPOINT", s3Endpoint)), S3Region: envDefault("S3_REGION", "us-east-1"), S3Bucket: strings.TrimSpace(os.Getenv("S3_BUCKET")), S3AccessKeyID: os.Getenv("S3_ACCESS_KEY_ID"), S3SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"), S3ServerSideEncryption: strings.ToUpper(envDefault("S3_SERVER_SIDE_ENCRYPTION", "AES256")), MediaURLTTL: time.Duration(envInt("MEDIA_URL_TTL_SECONDS", 300)) * time.Second, MediaCleanupInterval: time.Duration(envInt("MEDIA_CLEANUP_INTERVAL_SECONDS", 60)) * time.Second, MediaUploadGlobalLimit: envInt("MEDIA_UPLOAD_GLOBAL_CONCURRENCY", 8), MediaUploadActorLimit: envInt("MEDIA_UPLOAD_ACTOR_CONCURRENCY", 2),
		RetentionInterval: time.Duration(envInt("RETENTION_INTERVAL_SECONDS", 300)) * time.Second, RetentionBatchSize: envInt("RETENTION_BATCH_SIZE", 500), PreviewRetention: time.Duration(envInt("PREVIEW_RETENTION_HOURS", 168)) * time.Hour, IdempotencyRetention: time.Duration(envInt("IDEMPOTENCY_RETENTION_HOURS", 720)) * time.Hour, SessionRetention: time.Duration(envInt("SESSION_RETENTION_HOURS", 720)) * time.Hour, IdentityTokenRetention: time.Duration(envInt("IDENTITY_TOKEN_RETENTION_HOURS", 168)) * time.Hour, OutboxRedactAfter: time.Duration(envInt("OUTBOX_REDACT_AFTER_HOURS", 168)) * time.Hour, OutboxRetention: time.Duration(envInt("OUTBOX_RETENTION_HOURS", 720)) * time.Hour,
		GitCommit: envDefault("GIT_COMMIT", "0000000"), ExpectedSchemaVersion: envDefault("EXPECTED_SCHEMA_VERSION", "0019"),
		Port: envDefault("PORT", "8080"), TrustedProxyCount: envInt("TRUSTED_PROXY_COUNT", 0),
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		RateLimitProvider: strings.ToLower(envDefault("RATE_LIMIT_PROVIDER", "memory")), RedisURL: strings.TrimSpace(os.Getenv("REDIS_URL")), RateLimitPrefix: strings.TrimSpace(os.Getenv("RATE_LIMIT_PREFIX")), RateLimitTimeout: time.Duration(envInt("RATE_LIMIT_TIMEOUT_MS", 200)) * time.Millisecond, RateLimitDevFallback: envBool("RATE_LIMIT_DEV_FALLBACK", false),
		MercadoPagoProvider: strings.ToLower(envDefault("MERCADO_PAGO_PROVIDER", "disabled")), MercadoPagoAccessToken: strings.TrimSpace(os.Getenv("MERCADO_PAGO_ACCESS_TOKEN")), MercadoPagoWebhookSecret: strings.TrimSpace(os.Getenv("MERCADO_PAGO_WEBHOOK_SECRET")), MercadoPagoAPIURL: strings.TrimRight(envDefault("MERCADO_PAGO_API_URL", "https://api.mercadopago.com"), "/"), MercadoPagoBranchPrice: int64(envInt("MERCADO_PAGO_BRANCH_PRICE_CENTS", 1500000)), MercadoPagoPointsPrice: int64(envInt("MERCADO_PAGO_POINTS_BRANCH_PRICE_CENTS", 2000000)), MercadoPagoTimeout: time.Duration(envInt("MERCADO_PAGO_TIMEOUT_MS", 10000)) * time.Millisecond,
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return Config{}, errors.New("JWT_SECRET must be at least 32 bytes")
	}
	if c.JWTIssuer == "" {
		return Config{}, errors.New("JWT_ISSUER must not be empty")
	}
	if len(c.QRPepper) < 32 {
		return Config{}, errors.New("QR_PEPPER must be at least 32 bytes")
	}
	if c.JWTSecret == c.QRPepper {
		return Config{}, errors.New("JWT_SECRET and QR_PEPPER must differ")
	}
	if !fourDigits(c.ExpectedSchemaVersion) {
		return Config{}, errors.New("EXPECTED_SCHEMA_VERSION must have four digits")
	}
	appURL, err := url.Parse(c.PublicAppURL)
	if err != nil || appURL.Host == "" || (appURL.Scheme != "http" && appURL.Scheme != "https") {
		return Config{}, errors.New("PUBLIC_APP_URL must be an absolute http(s) URL")
	}
	production := strings.EqualFold(os.Getenv("APP_ENV"), "production")
	c.Production = production
	if encodedKey := strings.TrimSpace(os.Getenv("OUTBOX_ENCRYPTION_KEY")); encodedKey != "" {
		c.OutboxEncryptionKey, err = base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(c.OutboxEncryptionKey) != 32 {
			return Config{}, errors.New("OUTBOX_ENCRYPTION_KEY must be base64 for exactly 32 bytes")
		}
	}
	if production && !c.EmailVerificationRequired {
		return Config{}, errors.New("EMAIL_VERIFICATION_REQUIRED=true is required in production")
	}
	if production && (appURL.Scheme != "https" || appURL.Hostname() == "" || appURL.User != nil || appURL.RawQuery != "" || appURL.Fragment != "") {
		return Config{}, errors.New("PUBLIC_APP_URL must be a clean HTTPS origin in production")
	}
	if c.EmailVerificationRequired && len(c.OutboxEncryptionKey) != 32 {
		return Config{}, errors.New("OUTBOX_ENCRYPTION_KEY is required while email verification is required")
	}
	if production && (string(c.OutboxEncryptionKey) == c.JWTSecret || string(c.OutboxEncryptionKey) == c.QRPepper) {
		return Config{}, errors.New("a separate 32-byte OUTBOX_ENCRYPTION_KEY is required in production")
	}
	if c.MailProvider != "disabled" && c.MailProvider != "smtp" {
		return Config{}, errors.New("MAIL_PROVIDER must be disabled or smtp")
	}
	if c.EmailVerificationRequired && c.MailProvider != "smtp" {
		return Config{}, errors.New("MAIL_PROVIDER=smtp is required while email verification is required")
	}
	if c.MailProvider == "smtp" {
		if c.MailFromAddress == "" || c.SMTPHost == "" || c.SMTPPort < 1 || c.SMTPPort > 65535 {
			return Config{}, errors.New("MAIL_FROM_ADDRESS, SMTP_HOST and valid SMTP_PORT are required")
		}
		if c.SMTPTLSMode != "starttls" && c.SMTPTLSMode != "tls" {
			return Config{}, errors.New("SMTP_TLS_MODE must be starttls or tls")
		}
		if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
			return Config{}, errors.New("SMTP_USERNAME and SMTP_PASSWORD must be configured together")
		}
		from, mailErr := mail.ParseAddress(c.MailFromAddress)
		if mailErr != nil || from.Address != c.MailFromAddress || strings.ContainsAny(c.MailFromName, "\r\n") {
			return Config{}, errors.New("mail sender identity is invalid")
		}
	}
	if c.MediaProvider != "disabled" && c.MediaProvider != "s3" {
		return Config{}, errors.New("MEDIA_PROVIDER must be disabled or s3")
	}
	if production && c.MediaProvider != "s3" {
		return Config{}, errors.New("MEDIA_PROVIDER=s3 is required in production")
	}
	if c.MediaProvider == "s3" {
		endpoint, endpointErr := url.Parse(c.S3Endpoint)
		if endpointErr != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || c.S3Bucket == "" || c.S3Region == "" || c.S3AccessKeyID == "" || c.S3SecretAccessKey == "" {
			return Config{}, errors.New("valid S3 endpoint, region, bucket and credentials are required")
		}
		publicEndpoint, publicEndpointErr := url.Parse(c.S3PublicEndpoint)
		if publicEndpointErr != nil || publicEndpoint.Host == "" || publicEndpoint.User != nil || publicEndpoint.RawQuery != "" || publicEndpoint.Fragment != "" || (publicEndpoint.Scheme != "http" && publicEndpoint.Scheme != "https") {
			return Config{}, errors.New("S3_PUBLIC_ENDPOINT must be a clean absolute HTTP(S) URL")
		}
		if production && endpoint.Scheme != "https" {
			return Config{}, errors.New("S3_ENDPOINT must use HTTPS in production")
		}
		if production && publicEndpoint.Scheme != "https" {
			return Config{}, errors.New("S3_PUBLIC_ENDPOINT must use HTTPS in production")
		}
		if c.S3ServerSideEncryption != "AES256" && c.S3ServerSideEncryption != "DISABLED" {
			return Config{}, errors.New("S3_SERVER_SIDE_ENCRYPTION must be AES256 or DISABLED")
		}
		if production && c.S3ServerSideEncryption != "AES256" {
			return Config{}, errors.New("S3_SERVER_SIDE_ENCRYPTION=AES256 is required in production")
		}
	}
	if c.MediaURLTTL < time.Minute || c.MediaURLTTL > 15*time.Minute {
		return Config{}, errors.New("MEDIA_URL_TTL_SECONDS must be between 60 and 900")
	}
	if c.MediaCleanupInterval < 5*time.Second || c.MediaCleanupInterval > time.Hour {
		return Config{}, errors.New("MEDIA_CLEANUP_INTERVAL_SECONDS must be between 5 and 3600")
	}
	if c.MediaUploadGlobalLimit < 1 || c.MediaUploadGlobalLimit > 64 || c.MediaUploadActorLimit < 1 || c.MediaUploadActorLimit > c.MediaUploadGlobalLimit {
		return Config{}, errors.New("media upload concurrency limits are invalid")
	}
	if c.RetentionInterval < 10*time.Second || c.RetentionInterval > 24*time.Hour || c.RetentionBatchSize < 1 || c.RetentionBatchSize > 5000 {
		return Config{}, errors.New("retention interval or batch size is outside the safe range")
	}
	if c.RateLimitProvider != "memory" && c.RateLimitProvider != "redis" {
		return Config{}, errors.New("RATE_LIMIT_PROVIDER must be memory or redis")
	}
	if production && c.RateLimitProvider != "redis" {
		return Config{}, errors.New("RATE_LIMIT_PROVIDER=redis is required in production")
	}
	if c.RateLimitProvider == "redis" {
		redisURL, redisErr := url.Parse(c.RedisURL)
		if redisErr != nil || redisURL.Host == "" || (redisURL.Scheme != "redis" && redisURL.Scheme != "rediss") || c.RateLimitPrefix == "" || strings.ContainsAny(c.RateLimitPrefix, " \t\r\n") {
			return Config{}, errors.New("valid REDIS_URL and environment-specific RATE_LIMIT_PREFIX are required")
		}
		if production && redisURL.Scheme != "rediss" {
			return Config{}, errors.New("REDIS_URL must use TLS in production")
		}
	}
	if c.RateLimitTimeout < 50*time.Millisecond || c.RateLimitTimeout > 2*time.Second {
		return Config{}, errors.New("RATE_LIMIT_TIMEOUT_MS must be between 50 and 2000")
	}
	if c.MercadoPagoProvider != "disabled" && c.MercadoPagoProvider != "api" {
		return Config{}, errors.New("MERCADO_PAGO_PROVIDER must be disabled or api")
	}
	if c.MercadoPagoBranchPrice < 1 || c.MercadoPagoPointsPrice < 1 || c.MercadoPagoTimeout < time.Second || c.MercadoPagoTimeout > 30*time.Second {
		return Config{}, errors.New("Mercado Pago price or timeout is outside the safe range")
	}
	if c.MercadoPagoProvider == "api" {
		providerURL, providerErr := url.Parse(c.MercadoPagoAPIURL)
		if providerErr != nil || providerURL.Host == "" || providerURL.User != nil || providerURL.RawQuery != "" || providerURL.Fragment != "" || (providerURL.Scheme != "http" && providerURL.Scheme != "https") || c.MercadoPagoAccessToken == "" || c.MercadoPagoWebhookSecret == "" {
			return Config{}, errors.New("Mercado Pago API URL, access token and webhook secret are required")
		}
		if production && providerURL.Scheme != "https" {
			return Config{}, errors.New("MERCADO_PAGO_API_URL must use HTTPS in production")
		}
	}
	if c.PreviewRetention < time.Hour || c.IdempotencyRetention < 24*time.Hour || c.SessionRetention < 24*time.Hour || c.IdentityTokenRetention < time.Hour || c.OutboxRedactAfter < time.Hour || c.OutboxRetention < c.OutboxRedactAfter {
		return Config{}, errors.New("retention windows are outside the safe range")
	}
	return c, nil
}

func fourDigits(value string) bool {
	if len(value) != 4 {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

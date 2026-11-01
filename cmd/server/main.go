package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"clientesFrecuentes/internal/applebilling"
	"clientesFrecuentes/internal/appleidentity"
	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/cardevents"
	"clientesFrecuentes/internal/challenge"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/logging"
	"clientesFrecuentes/internal/mailer"
	"clientesFrecuentes/internal/maintenance"
	"clientesFrecuentes/internal/mercadopago"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/password"
	"clientesFrecuentes/internal/push"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var retainedLogs *logging.Daily
	if directory := os.Getenv("LOG_DIRECTORY"); directory != "" {
		var logErr error
		retainedLogs, logErr = logging.New(directory)
		if logErr != nil {
			logger.Error("log retention initialization failed")
			os.Exit(1)
		}
		logger = slog.New(slog.NewJSONHandler(retainedLogs, nil))
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	password.Configure(cfg.PasswordConcurrency)
	poolConfig, err := cfg.PoolConfig()
	if err != nil {
		logger.Error("invalid database configuration", "error", err)
		os.Exit(1)
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.NewWithConfig(startupCtx, poolConfig)
	cancel()
	if err != nil {
		logger.Error("database pool failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	repo := repository.New(pool, cfg.OutboxEncryptionKey)
	repo.AppleBillingEnvironment = cfg.AppleBillingEnvironment
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	if retainedLogs != nil {
		go retainedLogs.Run(workerCtx)
	}
	tokens := auth.NewTokens(cfg.JWTSecret, cfg.JWTIssuer)
	cardEvents := cardevents.New(poolConfig.ConnConfig, logger)
	go cardEvents.Run(workerCtx)
	limiter := middleware.NewRateLimiter()
	if cfg.RateLimitProvider == "redis" {
		limiter, err = middleware.NewRedisRateLimiter(cfg.RedisURL, cfg.RateLimitPrefix, cfg.RateLimitTimeout, !cfg.Production && cfg.RateLimitDevFallback)
		if err != nil {
			logger.Error("rate limiter configuration failed", "error", err)
			os.Exit(1)
		}
	}
	defer limiter.Close()
	var mediaStore service.MediaStore
	if cfg.MediaProvider == "s3" {
		mediaStore, err = storage.NewS3(context.Background(), cfg)
		if err != nil {
			logger.Error("media storage failed", "error", err)
			os.Exit(1)
		}
		storageCtx, storageCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = mediaStore.Ready(storageCtx)
		storageCancel()
		if err != nil {
			logger.Error("media storage readiness failed", "error", err)
			os.Exit(1)
		}
	}
	if cfg.MailProvider != "disabled" {
		var sender mailer.Sender = mailer.NewSMTP(cfg)
		if cfg.MailProvider == "capture" {
			sender = mailer.CaptureSender{Directory: cfg.MailCaptureDirectory, FromName: cfg.MailFromName, FromAddress: cfg.MailFromAddress}
		}
		go (mailer.Worker{GlobalHourlyBudget: cfg.MailGlobalHourlyBudget, RecipientHourlyBudget: cfg.MailRecipientHourlyBudget, Budget: limiter, Repo: repo, Sender: sender, Logger: logger, Interval: cfg.MailPollInterval, PublicAppURL: cfg.PublicAppURL, CipherKey: cfg.OutboxEncryptionKey, LogoStore: mediaStore}).Run(workerCtx)
	}
	go (push.Worker{Repo: repo, Sender: push.NewClient(&http.Client{Timeout: 10 * time.Second}), Logger: logger, Interval: time.Second}).Run(workerCtx)
	go (push.WebWorker{Repo: repo, Logger: logger, Subject: cfg.PublicAppURL}).Run(workerCtx)
	go (maintenance.Worker{Repo: repo, Store: mediaStore, Logger: logger, Config: cfg}).Run(workerCtx)
	svc := service.New(repo, tokens, cfg, mediaStore)
	identity, err := appleidentity.New(appleidentity.Config{ClientID: cfg.AppleIdentityClientID, TeamID: cfg.AppleIdentityTeamID, KeyID: cfg.AppleIdentityKeyID, PrivateKeyPEM: string(cfg.AppleIdentityPrivateKeyPEM), RedirectURI: cfg.AppleIdentityRedirectURI})
	if err != nil {
		logger.Error("invalid Apple identity configuration")
		os.Exit(1)
	}
	if identity != nil {
		svc.AppleIdentity = identity
	}
	appleBilling, err := applebilling.New(applebilling.Config{Enabled: cfg.AppleBillingEnabled, PurchasesEnabled: cfg.AppleIAPPurchasesEnabled, SubscriptionGroupID: cfg.AppleBillingSubscriptionGroupID, Environment: cfg.AppleBillingEnvironment, BundleID: "com.puntazo.app", AppAppleID: cfg.AppleBillingAppAppleID, IssuerID: cfg.AppleBillingIssuerID, KeyID: cfg.AppleBillingKeyID, PrivateKeyPEM: cfg.AppleBillingPrivateKeyPEM, RootsPEM: cfg.AppleBillingRootsPEM, AllowXcode: cfg.AppleBillingAllowXcode, Production: cfg.Production}, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		logger.Error("invalid Apple billing configuration")
		os.Exit(1)
	}
	svc.AppleBilling = appleBilling
	go svc.RunAppleRevocations(workerCtx)
	go svc.RunAppleBilling(workerCtx, logger)
	svc.Challenge = &challenge.Manager{Enabled: cfg.CaptchaEnabled, SiteKey: cfg.CaptchaSiteKey, Secret: cfg.CaptchaSecret, Hostnames: cfg.CaptchaHostnames, Store: limiter}
	if cfg.MercadoPagoProvider == "api" {
		svc.Billing = mercadopago.New(cfg.MercadoPagoAPIURL, cfg.MercadoPagoAccessToken, cfg.MercadoPagoTimeout)
	}
	go svc.RunSubscriptionPriceChanges(workerCtx, logger)
	go svc.RunBranchProration(workerCtx, logger)
	trustedProxyCIDRs, _ := middleware.ParseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	h := &handler.Handler{TrustedProxyCIDRs: trustedProxyCIDRs, Service: svc, Repo: repo, Tokens: tokens, CardEvents: cardEvents, Limiter: limiter, Uploads: middleware.NewUploadSemaphore(cfg.MediaUploadGlobalLimit, cfg.MediaUploadActorLimit), Logger: logger, TrustedProxyCount: cfg.TrustedProxyCount}
	go logCapacity(workerCtx, pool, h.Uploads, limiter, cardEvents, logger)
	router := newRouter(h, tokens, logger)
	listenAddress := ":" + cfg.Port
	if cfg.BranchPaymentSimulator || (cfg.BranchProrationEnabled && !cfg.Production) {
		listenAddress = "127.0.0.1:" + cfg.Port
	}
	server := &http.Server{Addr: listenAddress, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 32 << 10}
	go func() {
		logger.Info("server starting", "port", cfg.Port, "version", cfg.AppVersion)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	stopWorker() // Close active event streams before HTTP graceful shutdown.
	ctx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

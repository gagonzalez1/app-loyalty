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

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/mailer"
	"clientesFrecuentes/internal/maintenance"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
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
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	tokens := auth.NewTokens(cfg.JWTSecret, cfg.JWTIssuer)
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
	if cfg.MailProvider == "smtp" {
		go (mailer.Worker{Repo: repo, Sender: mailer.NewSMTP(cfg), Logger: logger, Interval: cfg.MailPollInterval, PublicAppURL: cfg.PublicAppURL, CipherKey: cfg.OutboxEncryptionKey, LogoStore: mediaStore}).Run(workerCtx)
	}
	go (maintenance.Worker{Repo: repo, Store: mediaStore, Logger: logger, Config: cfg}).Run(workerCtx)
	svc := service.New(repo, tokens, cfg, mediaStore)
	h := &handler.Handler{Service: svc, Repo: repo, Limiter: limiter, Uploads: middleware.NewUploadSemaphore(cfg.MediaUploadGlobalLimit, cfg.MediaUploadActorLimit), Logger: logger, TrustedProxyCount: cfg.TrustedProxyCount}
	router := newRouter(h, tokens, logger)
	server := &http.Server{Addr: ":" + cfg.Port, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 32 << 10}
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
	ctx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

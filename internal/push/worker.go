package push

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"clientesFrecuentes/internal/repository"
)

type Sender interface {
	Send(context.Context, string) error
}

type Worker struct {
	Repo     *repository.Repository
	Sender   Sender
	Logger   *slog.Logger
	Interval time.Duration
}

func (w Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastCleanup := time.Time{}
	for {
		w.process(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(lastCleanup) >= time.Hour {
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := w.Repo.CleanupPushJobs(cleanupCtx); err != nil {
				w.Logger.Error("push cleanup failed", "error", err)
			}
			cancel()
			lastCleanup = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w Worker) process(ctx context.Context) {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	jobs, err := w.Repo.ClaimPushJobs(claimCtx, 5)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			w.Logger.Error("push job claim failed", "error", err)
		}
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 10*time.Second)
		err := w.Sender.Send(sendCtx, job.Token)
		sendCancel()
		resultCtx, resultCancel := context.WithTimeout(ctx, 5*time.Second)
		switch {
		case err == nil:
			if e := w.Repo.CompletePushJob(resultCtx, job.ID, job.Attempts); e != nil {
				w.Logger.Error("push job completion failed", "job_id", job.ID, "error", e)
			}
		case errors.Is(err, ErrDeviceNotRegistered):
			if e := w.Repo.DeleteInvalidPushToken(resultCtx, job.TokenID, job.CustomerID, job.Token); e != nil {
				w.Logger.Error("invalid push token removal failed", "token_id", job.TokenID, "error", e)
			} else {
				w.Logger.Warn("invalid push token removed", "token_id", job.TokenID, "customer_id", job.CustomerID)
			}
		default:
			w.Logger.Error("expo push delivery failed", "job_id", job.ID, "customer_id", job.CustomerID, "attempt", job.Attempts, "error", err)
			if e := w.Repo.FailPushJob(resultCtx, job.ID, job.Attempts, err.Error()); e != nil {
				w.Logger.Error("push job retry update failed", "job_id", job.ID, "error", e)
			}
		}
		resultCancel()
	}
}

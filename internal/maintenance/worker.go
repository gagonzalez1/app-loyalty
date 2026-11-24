package maintenance

import (
	"context"
	"log/slog"
	"time"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/repository"
	"github.com/google/uuid"
)

type Store interface {
	Delete(context.Context, string) error
}

type Observer interface {
	ObserveRetention(kind string, count int64, elapsed time.Duration, err error)
}

type Worker struct {
	Repo       *repository.Repository
	Store      Store
	Logger     *slog.Logger
	Config     config.Config
	Observer   Observer
	Now        func() time.Time
	LeaseOwner string
}

func (w Worker) Run(ctx context.Context) {
	if w.LeaseOwner == "" {
		w.LeaseOwner = uuid.NewString()
	}
	retentionTicker := time.NewTicker(w.Config.RetentionInterval)
	defer retentionTicker.Stop()
	mediaTicker := time.NewTicker(w.Config.MediaCleanupInterval)
	defer mediaTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-retentionTicker.C:
			w.RunRetention(ctx)
		case <-mediaTicker.C:
			w.RunMediaCleanup(ctx)
		}
	}
}

func (w Worker) RunRetention(ctx context.Context) {
	started := time.Now()
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	stats, err := w.Repo.ApplyRetention(ctx, repository.RetentionPolicy{Now: now, BatchSize: w.Config.RetentionBatchSize, PreviewRetention: w.Config.PreviewRetention, IdempotencyRetention: w.Config.IdempotencyRetention, SessionRetention: w.Config.SessionRetention, IdentityRetention: w.Config.IdentityTokenRetention, OutboxRedactAfter: w.Config.OutboxRedactAfter, OutboxRetention: w.Config.OutboxRetention})
	if err != nil {
		w.Logger.Error("retention batch failed", "error", err)
	} else {
		w.Logger.Info("retention batch completed", "previews", stats.PreviewsDeleted, "idempotencies", stats.IdempotenciesDeleted, "sessions", stats.SessionsDeleted, "identity_tokens", stats.IdentityTokensDeleted, "google_browser_flows", stats.GoogleBrowserFlowsDeleted, "outbox_redacted", stats.OutboxRedacted, "outbox_deleted", stats.OutboxDeleted)
	}
	if w.Observer != nil {
		w.Observer.ObserveRetention("database", stats.PreviewsDeleted+stats.IdempotenciesDeleted+stats.SessionsDeleted+stats.IdentityTokensDeleted+stats.GoogleBrowserFlowsDeleted+stats.OutboxRedacted+stats.OutboxDeleted, time.Since(started), err)
	}
}

func (w Worker) RunMediaCleanup(ctx context.Context) {
	if w.Store == nil {
		return
	}
	started := time.Now()
	leaseOwner := w.LeaseOwner
	keys, profileErr := w.Repo.PendingProfileDeletions(ctx, w.Config.RetentionBatchSize)
	if profileErr != nil {
		w.Logger.Error("profile deletion lookup failed", "error", profileErr)
	}
	for _, key := range keys {
		if err := w.Store.Delete(ctx, key); err != nil {
			w.Logger.Warn("profile deletion will retry")
			continue
		}
		if err := w.Repo.CompleteProfileDeletion(ctx, key); err != nil {
			w.Logger.Warn("profile deletion completion will retry")
		}
	}
	if leaseOwner == "" {
		leaseOwner = uuid.NewString()
	}
	w.runMenuCleanup(ctx, leaseOwner)
	items, err := w.Repo.DueBrandMedia(ctx, w.Config.RetentionBatchSize, leaseOwner)
	if err != nil {
		w.Logger.Error("media cleanup claim failed", "error", err)
		if w.Observer != nil {
			w.Observer.ObserveRetention("media", 0, time.Since(started), err)
		}
		return
	}
	var completed int64
	var batchErr error
	for _, item := range items {
		failure := ""
		deleteErr := w.Store.Delete(ctx, item.ObjectKey)
		if deleteErr != nil {
			failure = deleteErr.Error()
			batchErr = deleteErr
			w.Logger.Warn("media object deletion failed", "image_id", item.ID, "status", item.Status, "error", deleteErr)
		}
		if completionErr := w.Repo.CompleteBrandMediaDeletion(ctx, item.ID, leaseOwner, failure); completionErr != nil {
			w.Logger.Error("media cleanup completion failed", "image_id", item.ID, "error", completionErr)
			batchErr = completionErr
		} else if failure == "" {
			completed++
		}
	}
	if w.Observer != nil {
		w.Observer.ObserveRetention("media", completed, time.Since(started), batchErr)
	}
}

func (w Worker) runMenuCleanup(ctx context.Context, owner string) {
	started := time.Now()
	items, err := w.Repo.DueMenuMedia(ctx, w.Config.RetentionBatchSize, owner)
	if err != nil {
		w.Logger.Error("menu cleanup claim failed")
		return
	}
	var completed int64
	var batchErr error
	for _, item := range items {
		deletionErr := w.Store.Delete(ctx, item.ObjectKey)
		if deletionErr != nil {
			batchErr = deletionErr
			w.Logger.Warn("menu cleanup will retry", "document_id", item.ID)
		}
		if err := w.Repo.CompleteMenuMediaDeletion(ctx, item.ID, owner, deletionErr != nil); err != nil {
			batchErr = err
			w.Logger.Error("menu cleanup completion will retry", "document_id", item.ID)
		} else if deletionErr == nil {
			completed++
		}
	}
	if w.Observer != nil {
		w.Observer.ObserveRetention("menu_media", completed, time.Since(started), batchErr)
	}
}

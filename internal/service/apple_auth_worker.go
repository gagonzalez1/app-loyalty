package service

import (
	"context"
	"time"

	"clientesFrecuentes/internal/appleidentity"
)

func (s *Service) ProcessAppleRevocations(ctx context.Context, limit int) (int, error) {
	if s.Repo == nil {
		return 0, ErrAppleAuthUnavailable
	}
	if err := s.Repo.PruneAppleChallenges(ctx, s.Now()); err != nil {
		return 0, err
	}
	if s.AppleIdentity == nil || !s.AppleIdentity.Enabled() {
		return 0, ErrAppleAuthUnavailable
	}
	items, err := s.Repo.ClaimAppleRevocations(ctx, limit, s.Now())
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, item := range items {
		if ctx.Err() != nil {
			return completed, ctx.Err()
		}
		token, e := appleidentity.DecryptToken(s.Repo.OutboxCipherKey, item.ClientID, item.TokenType, item.Ciphertext, item.Nonce)
		if e == nil {
			e = s.AppleIdentity.Revoke(ctx, token, item.TokenType)
		}
		if e != nil {
			if retryErr := s.Repo.RetryAppleRevocation(ctx, item, s.Now()); retryErr != nil {
				return completed, retryErr
			}
			continue
		}
		if err = s.Repo.CompleteAppleRevocation(ctx, item); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

// RunAppleRevocations survives individual provider failures; durable leases make
// retries safe across restarts and multiple API replicas. Bootstrap owns logs.
func (s *Service) RunAppleRevocations(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_, _ = s.ProcessAppleRevocations(ctx, 20)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

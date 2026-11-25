package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"

	"github.com/google/uuid"
)

func (s *Service) CreateGiftCardBatch(ctx context.Context, actorID, brandID int64, req model.GiftCardCreateRequest) (model.GiftCardBatch, error) {
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" || len([]rune(req.Title)) > 80 || req.BranchID < 1 || req.Amount < 1 || req.Amount > 100000 || req.Quantity < 1 || req.Quantity > 100 {
		return model.GiftCardBatch{}, ErrInvalidRequest
	}
	if req.ExpiresAt != nil {
		value := req.ExpiresAt.UTC()
		if !value.After(s.Now().UTC().Add(time.Minute)) {
			return model.GiftCardBatch{}, ErrInvalidRequest
		}
		req.ExpiresAt = &value
	}
	tokens := make([]repository.GiftCardToken, 0, req.Quantity)
	for range req.Quantity {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return model.GiftCardBatch{}, err
		}
		token := "pgc_" + base64.RawURLEncoding.EncodeToString(raw)
		hash := repository.GiftCardHash(token)
		tokens = append(tokens, repository.GiftCardToken{ID: uuid.NewString(), Token: token, Hint: token[len(token)-6:], Hash: hash})
	}
	return s.Repo.CreateGiftCardBatch(ctx, actorID, brandID, req, tokens, s.Config.PublicAppURL)
}

func (s *Service) ListGiftCardBatches(ctx context.Context, actorID, brandID int64) ([]model.GiftCardBatch, error) {
	return s.Repo.ListGiftCardBatches(ctx, actorID, brandID)
}

func (s *Service) GiftCardBatch(ctx context.Context, actorID, brandID int64, batchID string) (model.GiftCardBatch, error) {
	if _, err := uuid.Parse(batchID); err != nil {
		return model.GiftCardBatch{}, ErrInvalidRequest
	}
	return s.Repo.GiftCardBatch(ctx, actorID, brandID, batchID, s.Config.PublicAppURL)
}

func (s *Service) RevokeGiftCardBatch(ctx context.Context, actorID, brandID int64, batchID string) error {
	if _, err := uuid.Parse(batchID); err != nil {
		return ErrInvalidRequest
	}
	return s.Repo.RevokeGiftCardBatch(ctx, actorID, brandID, batchID)
}

func (s *Service) DeleteGiftCardBatch(ctx context.Context, actorID, brandID int64, batchID string) error {
	if _, err := uuid.Parse(batchID); err != nil {
		return ErrInvalidRequest
	}
	return s.Repo.DeleteGiftCardBatch(ctx, actorID, brandID, batchID)
}

func (s *Service) GiftCardClaim(ctx context.Context, token string) (model.GiftCardClaim, error) {
	if !validGiftCardToken(token) {
		return model.GiftCardClaim{}, ErrInvalidRequest
	}
	return s.Repo.GiftCardClaim(ctx, token)
}

func (s *Service) RedeemGiftCard(ctx context.Context, customerID int64, token string) (model.GiftCardRedemption, error) {
	if !validGiftCardToken(token) {
		return model.GiftCardRedemption{}, ErrInvalidRequest
	}
	return s.Repo.RedeemGiftCard(ctx, customerID, token)
}

func validGiftCardToken(token string) bool {
	return strings.HasPrefix(token, "pgc_") && len(token) == 47
}

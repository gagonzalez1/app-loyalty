package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"clientesFrecuentes/internal/model"
)

func (s *Service) UpdateEstudioContact(ctx context.Context, actor, brand, branch int64, version int, patch model.EstudioContactPatch) (model.EstudioContact, error) {
	if patch.Address == nil && patch.WhatsApp == nil && patch.Instagram == nil && patch.TikTok == nil && patch.Hours == nil {
		return model.EstudioContact{}, ErrInvalidRequest
	}
	if patch.Address != nil {
		*patch.Address = strings.TrimSpace(*patch.Address)
		if utf8.RuneCountInString(*patch.Address) > 300 {
			return model.EstudioContact{}, ErrInvalidRequest
		}
	}
	profile := model.BranchPublicProfile{Links: make(map[string]string)}
	if patch.Hours != nil {
		profile.Hours = *patch.Hours
	}
	if patch.WhatsApp != nil {
		profile.WhatsApp = *patch.WhatsApp
	}
	for key, value := range map[string]*string{"instagram": patch.Instagram, "tiktok": patch.TikTok} {
		if value != nil {
			profile.Links[key] = *value
		}
	}
	if err := cleanBranchPublicProfile(&profile); err != nil {
		return model.EstudioContact{}, err
	}
	if patch.WhatsApp != nil {
		*patch.WhatsApp = profile.WhatsApp
	}
	if patch.Instagram != nil {
		*patch.Instagram = profile.Links["instagram"]
	}
	if patch.TikTok != nil {
		*patch.TikTok = profile.Links["tiktok"]
	}
	return s.Repo.UpdateEstudioContact(ctx, actor, brand, branch, version, patch)
}

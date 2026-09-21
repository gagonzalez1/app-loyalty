package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"

	"github.com/google/uuid"
)

const profilePhotoReferencePrefix = "s3://puntazo/"

func profilePhotoObjectKey(reference string) (string, bool) {
	if !strings.HasPrefix(reference, profilePhotoReferencePrefix) {
		return "", false
	}
	key := strings.TrimPrefix(reference, profilePhotoReferencePrefix)
	return key, strings.HasPrefix(key, "profiles/") && !strings.Contains(key, "..")
}

func profilePhotoMIME(key string) string {
	if strings.HasSuffix(strings.ToLower(key), ".jpg") || strings.HasSuffix(strings.ToLower(key), ".jpeg") {
		return "image/jpeg"
	}
	return "image/png"
}

func (s *Service) ProfilePhoto(ctx context.Context, actorID int64) (model.ProfilePhoto, error) {
	if s.Media == nil {
		return model.ProfilePhoto{}, ErrMediaUnavailable
	}
	reference, err := s.Repo.ProfilePhotoReference(ctx, actorID)
	if err != nil {
		return model.ProfilePhoto{}, err
	}
	if reference == nil || strings.TrimSpace(*reference) == "" {
		return model.ProfilePhoto{}, repository.ErrNotFound
	}
	if key, managed := profilePhotoObjectKey(*reference); managed {
		url, signErr := s.Media.SignedGet(ctx, key, s.Config.MediaURLTTL)
		if signErr != nil {
			return model.ProfilePhoto{}, ErrMediaUnavailable
		}
		return model.ProfilePhoto{URL: url, URLExpiresAt: s.Now().Add(s.Config.MediaURLTTL), MIMEType: profilePhotoMIME(key)}, nil
	}
	if strings.HasPrefix(*reference, "https://") || strings.HasPrefix(*reference, "http://") {
		return model.ProfilePhoto{URL: *reference, URLExpiresAt: s.Now().Add(time.Hour)}, nil
	}
	return model.ProfilePhoto{}, repository.ErrNotFound
}

func (s *Service) UploadProfilePhoto(ctx context.Context, actorID int64, expectedVersion int, input []byte) (model.ProfilePhotoUpdate, error) {
	if s.Media == nil {
		return model.ProfilePhotoUpdate{}, ErrMediaUnavailable
	}
	if expectedVersion < 1 {
		return model.ProfilePhotoUpdate{}, ErrInvalidRequest
	}
	body, mime, width, height, digest, err := normalizeImage(input, "ICONO")
	if err != nil {
		return model.ProfilePhotoUpdate{}, err
	}
	ext := "png"
	if mime == "image/jpeg" {
		ext = "jpg"
	}
	key := fmt.Sprintf("profiles/%d/%s.%s", actorID, uuid.NewString(), ext)
	if err = s.Media.Put(ctx, key, mime, body, digest); err != nil {
		return model.ProfilePhotoUpdate{}, ErrMediaUnavailable
	}
	compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	previous, current, err := s.Repo.ReplaceProfilePhoto(ctx, actorID, expectedVersion, profilePhotoReferencePrefix+key)
	if err != nil {
		_ = s.Media.Delete(compensationCtx, key)
		return model.ProfilePhotoUpdate{}, err
	}
	if previous != nil {
		if previousKey, managed := profilePhotoObjectKey(*previous); managed && previousKey != key {
			_ = s.Media.Delete(compensationCtx, previousKey)
		}
	}
	url, err := s.Media.SignedGet(ctx, key, s.Config.MediaURLTTL)
	if err != nil {
		return model.ProfilePhotoUpdate{}, ErrMediaUnavailable
	}
	return model.ProfilePhotoUpdate{
		Current: current,
		Photo: model.ProfilePhoto{
			URL: url, URLExpiresAt: s.Now().Add(s.Config.MediaURLTTL), MIMEType: mime,
			ByteSize: int64(len(body)), Width: width, Height: height,
		},
	}, nil
}

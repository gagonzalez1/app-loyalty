package service

import (
	"context"
	"regexp"
	"strings"
)

var expoTokenPattern = regexp.MustCompile(`^(ExpoPushToken|ExponentPushToken)\[[A-Za-z0-9_-]{1,200}\]$`)

func (s *Service) SavePushToken(ctx context.Context, customerID int64, token, deviceID string) error {
	token = strings.TrimSpace(token)
	if deviceID != "" && strings.TrimSpace(deviceID) == "" {
		return ErrInvalidRequest
	}
	deviceID = strings.TrimSpace(deviceID)
	if !expoTokenPattern.MatchString(token) || len(deviceID) > 128 {
		return ErrInvalidRequest
	}
	return s.Repo.SavePushToken(ctx, customerID, token, deviceID)
}

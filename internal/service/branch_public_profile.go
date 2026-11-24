package service

import (
	"clientesFrecuentes/internal/model"
	"net/url"
	"strings"
	"time"
)

const minutesPerDay = 24 * 60
const minutesPerWeek = 7 * minutesPerDay

func branchTimeMinutes(value string) (int, bool) {
	parsed, err := time.Parse("15:04", value)
	return parsed.Hour()*60 + parsed.Minute(), err == nil && parsed.Format("15:04") == value
}

func cleanBranchPublicProfile(profile *model.BranchPublicProfile) error {
	if profile == nil {
		return nil
	}
	if len(profile.Hours) > 28 || len(profile.Links) > 6 {
		return ErrInvalidRequest
	}
	var occupied [minutesPerWeek]bool
	for _, slot := range profile.Hours {
		opens, openOK := branchTimeMinutes(slot.Opens)
		closes, closeOK := branchTimeMinutes(slot.Closes)
		if len(slot.Days) == 0 || len(slot.Days) > 7 || !openOK || !closeOK || opens == closes {
			return ErrInvalidRequest
		}
		if closes < opens {
			closes += minutesPerDay
		}
		for _, day := range slot.Days {
			if day < 0 || day > 6 {
				return ErrInvalidRequest
			}
			for offset := opens; offset < closes; offset++ {
				minute := (day*minutesPerDay + offset) % minutesPerWeek
				if occupied[minute] {
					return ErrInvalidRequest
				}
				occupied[minute] = true
			}
		}
	}
	for key, raw := range profile.Links {
		var domain string
		switch key {
		case "sitio_web", "menu_ingles":
		case "instagram":
			domain = "instagram.com"
		case "facebook":
			domain = "facebook.com"
		case "tiktok":
			domain = "tiktok.com"
		case "linkedin":
			domain = "linkedin.com"
		default:
			return ErrInvalidRequest
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			delete(profile.Links, key)
			continue
		}
		if len(raw) > 2048 || strings.ContainsAny(raw, "\\\r\n\t") {
			return ErrInvalidRequest
		}
		link, err := url.Parse(raw)
		if err != nil || link.Scheme != "https" || link.User != nil || link.Port() != "" || !strings.Contains(link.Hostname(), ".") {
			return ErrInvalidRequest
		}
		host := strings.ToLower(link.Hostname())
		if domain != "" && host != domain && !strings.HasSuffix(host, "."+domain) {
			return ErrInvalidRequest
		}
		profile.Links[key] = link.String()
	}
	return nil
}

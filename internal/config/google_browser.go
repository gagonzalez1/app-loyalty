package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

func loadGoogleBrowserConfiguration(c *Config) error {
	c.GoogleBrowserRedirectURI = "" // Keep the redirect disabled in the testing recovery image.
	if c.GoogleBrowserRedirectURI == "" {
		return nil
	}
	c.GoogleBrowserClientID = strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID"))
	if c.GoogleBrowserClientID == "" || len(c.OutboxEncryptionKey) != 32 {
		return errors.New("Google browser auth requires the web Google client ID and outbox encryption key")
	}
	app, err := url.Parse(c.PublicAppURL)
	if err != nil || app.Scheme != "https" || app.Hostname() == "" || app.User != nil {
		return errors.New("Google browser auth requires an HTTPS public app URL")
	}
	callback, err := url.Parse(c.GoogleBrowserRedirectURI)
	if err != nil || callback.Scheme != "https" || !strings.EqualFold(callback.Host, app.Host) || callback.User != nil ||
		callback.RawQuery != "" || callback.ForceQuery || callback.Fragment != "" || callback.RawFragment != "" ||
		callback.EscapedPath() != "/api/v1/auth/google/browser/callback" {
		return errors.New("Google browser auth requires an HTTPS callback on the public app origin")
	}
	return nil
}

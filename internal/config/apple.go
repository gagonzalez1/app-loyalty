package config

import (
	"errors"
	"os"
	"strings"
)

func readAppleFile(name string) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" {
		return nil, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New(name + " cannot be read")
	}
	return content, nil
}

func loadAppleConfiguration(c *Config) error {
	c.AppleIdentityClientID = strings.TrimSpace(os.Getenv("APPLE_IDENTITY_CLIENT_ID"))
	c.AppleIdentityTeamID = strings.TrimSpace(os.Getenv("APPLE_IDENTITY_TEAM_ID"))
	c.AppleIdentityKeyID = strings.TrimSpace(os.Getenv("APPLE_IDENTITY_KEY_ID"))
	c.AppleIdentityRedirectURI = strings.TrimSpace(os.Getenv("APPLE_IDENTITY_REDIRECT_URI"))
	var err error
	c.AppleIdentityPrivateKeyPEM, err = readAppleFile("APPLE_IDENTITY_PRIVATE_KEY_PATH")
	if err != nil {
		return err
	}
	if c.AppleIdentityClientID != "" || c.AppleIdentityTeamID != "" || c.AppleIdentityKeyID != "" || len(c.AppleIdentityPrivateKeyPEM) > 0 {
		if c.AppleIdentityClientID == "" || c.AppleIdentityTeamID == "" || c.AppleIdentityKeyID == "" || len(c.AppleIdentityPrivateKeyPEM) == 0 || len(c.OutboxEncryptionKey) != 32 {
			return errors.New("Apple identity requires client/team/key IDs, private key and a separate outbox encryption key")
		}
	}
	return nil
}

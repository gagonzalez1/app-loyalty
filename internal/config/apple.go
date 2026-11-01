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
	c.AppleIAPPurchasesEnabled = envBool("APPLE_IAP_PURCHASES_ENABLED", false)
	c.AppleBillingEnabled = envBool("APPLE_BILLING_ENABLED", false)
	if c.AppleIAPPurchasesEnabled && !c.AppleBillingEnabled {
		return errors.New("Apple new purchases require configured billing provider")
	}
	c.AppleBillingEnvironment = envDefault("APPLE_BILLING_ENVIRONMENT", "Production")
	c.AppleBillingAppAppleID = int64(envInt("APPLE_BILLING_APP_APPLE_ID", 0))
	c.AppleBillingSubscriptionGroupID = strings.TrimSpace(os.Getenv("APPLE_BILLING_SUBSCRIPTION_GROUP_ID"))
	c.AppleBillingIssuerID = strings.TrimSpace(os.Getenv("APPLE_BILLING_ISSUER_ID"))
	c.AppleBillingKeyID = strings.TrimSpace(os.Getenv("APPLE_BILLING_KEY_ID"))
	c.AppleBillingAllowXcode = envBool("APPLE_BILLING_ALLOW_XCODE", false)
	c.AppleBillingPrivateKeyPEM, err = readAppleFile("APPLE_BILLING_PRIVATE_KEY_PATH")
	if err != nil {
		return err
	}
	c.AppleBillingRootsPEM, err = readAppleFile("APPLE_BILLING_ROOTS_PATH")
	if err != nil {
		return err
	}
	if c.Production && (c.AppleBillingEnvironment != "Production" || c.AppleBillingAllowXcode) {
		return errors.New("production cannot trust Apple Sandbox or Xcode transactions")
	}
	if c.AppleBillingAllowXcode && (!strings.EqualFold(os.Getenv("APP_ENV"), "development") || c.AppleBillingEnvironment != "Xcode") {
		return errors.New("Xcode billing requires isolated development environment")
	}
	return nil
}

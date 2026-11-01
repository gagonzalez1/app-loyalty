package config

import "testing"

func TestAppleConfigurationDefaultsDoNotEnablePurchases(t *testing.T) {
	c := &Config{}
	if err := loadAppleConfiguration(c); err != nil {
		t.Fatal(err)
	}
	if c.AppleBillingEnabled || c.AppleIAPPurchasesEnabled || c.AppleBillingAllowXcode || c.AppleBillingEnvironment != "Production" {
		t.Fatalf("unsafe Apple defaults: %+v", c)
	}
}

func TestAppleTrustEnvironmentAndPurchaseGates(t *testing.T) {
	for _, tt := range []struct {
		name, app, environment, xcode, purchases, enabled string
		production, allowed                               bool
	}{
		{"local Xcode", "development", "Xcode", "true", "true", "true", false, true},
		{"testing Xcode forbidden", "testing", "Xcode", "true", "false", "true", false, false},
		{"production Sandbox forbidden", "production", "Sandbox", "false", "false", "true", true, false},
		{"production Xcode forbidden", "production", "Production", "true", "false", "true", true, false},
		{"purchase without provider forbidden", "development", "Production", "false", "true", "false", false, false},
		{"process existing without new purchase", "production", "Production", "false", "false", "true", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tt.app)
			t.Setenv("APPLE_BILLING_ENVIRONMENT", tt.environment)
			t.Setenv("APPLE_BILLING_ALLOW_XCODE", tt.xcode)
			t.Setenv("APPLE_IAP_PURCHASES_ENABLED", tt.purchases)
			t.Setenv("APPLE_BILLING_ENABLED", tt.enabled)
			c := &Config{Production: tt.production}
			if err := loadAppleConfiguration(c); (err == nil) != tt.allowed {
				t.Fatalf("allowed=%v error=%v", tt.allowed, err)
			}
		})
	}
}

func TestAppleIdentityPartialCredentialsRejected(t *testing.T) {
	t.Setenv("APPLE_IDENTITY_CLIENT_ID", "com.puntazo.app")
	if err := loadAppleConfiguration(&Config{}); err == nil {
		t.Fatal("partial identity configuration accepted")
	}
}

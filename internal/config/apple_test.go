package config

import "testing"

func TestAppleIdentityAbsentConfigurationIsDisabled(t *testing.T) {
	c := &Config{}
	if err := loadAppleConfiguration(c); err != nil {
		t.Fatal(err)
	}
	if c.AppleIdentityClientID != "" || len(c.AppleIdentityPrivateKeyPEM) != 0 {
		t.Fatal("identity enabled without configuration")
	}
}
func TestAppleIdentityPartialCredentialsRejected(t *testing.T) {
	t.Setenv("APPLE_IDENTITY_CLIENT_ID", "com.puntazo.app")
	if err := loadAppleConfiguration(&Config{}); err == nil {
		t.Fatal("partial identity configuration accepted")
	}
}

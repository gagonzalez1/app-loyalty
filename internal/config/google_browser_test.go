package config

import "testing"

func TestGoogleBrowserConfigurationRequiresExactCallbackOriginAndPath(t *testing.T) {
	const valid = "https://testing.puntazo.test/api/v1/auth/google/browser/callback"
	t.Setenv("GOOGLE_CLIENT_ID", "web-client.apps.googleusercontent.com")
	for _, tc := range []struct {
		callback string
		valid    bool
	}{
		{valid, true},
		{"", true},
		{"https://other.example.test/api/v1/auth/google/browser/callback", false},
		{"http://testing.puntazo.test/api/v1/auth/google/browser/callback", false},
		{"https://testing.puntazo.test:9443/api/v1/auth/google/browser/callback", false},
		{"https://testing.puntazo.test/v1/auth/google/browser/callback", false},
		{valid + "?return_to=https://other.example.test", false},
		{valid + "#fragment", false},
	} {
		t.Run(tc.callback, func(t *testing.T) {
			t.Setenv("GOOGLE_BROWSER_REDIRECT_URI", tc.callback)
			c := Config{PublicAppURL: "https://testing.puntazo.test", OutboxEncryptionKey: make([]byte, 32)}
			err := loadGoogleBrowserConfiguration(&c)
			if (err == nil) != tc.valid {
				t.Fatalf("configuration error=%v, want valid=%v", err, tc.valid)
			}
		})
	}
	t.Setenv("GOOGLE_BROWSER_REDIRECT_URI", valid)
	if err := loadGoogleBrowserConfiguration(&Config{PublicAppURL: "https://testing.puntazo.test"}); err == nil {
		t.Fatal("enabled browser flow without proof encryption")
	}
}

package model

import "time"

type GoogleBrowserConfig struct {
	Enabled bool `json:"enabled"`
}

type GoogleBrowserStartRequest struct {
	CodeChallenge string `json:"code_challenge"`
}

type GoogleBrowserStart struct {
	FlowID           string    `json:"flow_id"`
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type GoogleBrowserFinishRequest struct {
	FlowID       string `json:"flow_id"`
	CodeVerifier string `json:"code_verifier"`
}

type GoogleBrowserFinish struct {
	IDToken string `json:"id_token"`
}

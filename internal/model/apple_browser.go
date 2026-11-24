package model

import "time"

type AppleBrowserStartRequest struct {
	Purpose       string `json:"purpose"`
	CodeChallenge string `json:"code_challenge"`
}

type AppleBrowserStart struct {
	FlowID           string    `json:"flow_id"`
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type AppleBrowserFinishRequest struct {
	FlowID       string `json:"flow_id"`
	CodeVerifier string `json:"code_verifier"`
	Version      int    `json:"version,omitempty"`
}

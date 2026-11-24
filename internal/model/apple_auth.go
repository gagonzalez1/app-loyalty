package model

import "time"

// AppleAuthProof contains one authorization response. It must never be logged.
type AppleAuthProof struct {
	ChallengeID       string `json:"challenge_id"`
	IDToken           string `json:"id_token"`
	AuthorizationCode string `json:"authorization_code"`
}

type AppleChallengeRequest struct {
	Purpose string `json:"purpose"`
}

type AppleChallenge struct {
	ChallengeID string    `json:"challenge_id"`
	Nonce       string    `json:"nonce"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type AppleAuthRequest struct {
	AppleAuthProof
	Name                 string                      `json:"name,omitempty"`
	LastName             string                      `json:"apellido,omitempty"`
	AccountType          *string                     `json:"account_type,omitempty"`
	TipoCuenta           *string                     `json:"tipo_cuenta,omitempty"`
	MerchantRegistration *GoogleMerchantRegistration `json:"merchant_registration,omitempty"`
}

type AppleLinkRequest struct {
	AppleAuthProof
	Version int `json:"version"`
}

// AppleIdentityExport excludes tokens, nonces and internal encryption material.
type AppleIdentityExport struct {
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
	PrivateRelay bool      `json:"private_relay"`
	LinkedAt     time.Time `json:"linked_at"`
}

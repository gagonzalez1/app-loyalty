package model

import "time"

type AppleProduct struct {
	ProductID   string `json:"product_id"`
	ProgramType string `json:"program_type"`
	Capacity    int    `json:"capacity"`
	Period      string `json:"period"`
}

type BrandBillingState struct {
	Mode             string     `json:"mode"`
	Status           string     `json:"status"`
	ProductID        string     `json:"product_id,omitempty"`
	ProgramType      string     `json:"program_type,omitempty"`
	Capacity         int        `json:"capacity"`
	ActiveBranches   int        `json:"active_branches"`
	CoveredUntil     *time.Time `json:"covered_until,omitempty"`
	GraceEndsAt      *time.Time `json:"grace_ends_at,omitempty"`
	AutoRenew        *bool      `json:"auto_renew,omitempty"`
	PendingProductID string     `json:"pending_product_id,omitempty"`
	Restriction      string     `json:"restriction,omitempty"`
	TrialEndsAt      *time.Time `json:"trial_ends_at,omitempty"`
}

type AppleCatalog struct {
	Enabled            bool           `json:"enabled"`
	Products           []AppleProduct `json:"products"`
	TrialEndsAt        *time.Time     `json:"trial_ends_at,omitempty"`
	EligibleToPurchase bool           `json:"eligible_to_purchase"`
}

type AppleOffer struct {
	OfferID string `json:"offer_id"`
	Code    string `json:"code,omitempty"`
}
type ApplePurchaseIntentRequest struct {
	ProductID    string `json:"product_id"`
	ReferralCode string `json:"referral_code,omitempty"`
}
type ApplePurchaseIntent struct {
	IntentID        string      `json:"intent_id"`
	AppAccountToken string      `json:"app_account_token"`
	ProductID       string      `json:"product_id"`
	ExpiresAt       time.Time   `json:"expires_at"`
	Offer           *AppleOffer `json:"offer,omitempty"`
}
type AppleSyncRequest struct {
	SignedTransaction string `json:"signed_transaction"`
	IntentID          string `json:"intent_id,omitempty"`
}

// Apple amounts are signed StoreKit milliunits, never MP's ARS minor units.
type AppleTransaction struct {
	RevocationReversed    bool   `json:"-"`
	TransactionID         string `json:"transactionId"`
	OriginalTransactionID string `json:"originalTransactionId"`
	BundleID              string `json:"bundleId"`
	ProductID             string `json:"productId"`
	SubscriptionGroupID   string `json:"subscriptionGroupIdentifier"`
	AppAccountToken       string `json:"appAccountToken"`
	Environment           string `json:"environment"`
	Type                  string `json:"type"`
	OwnershipType         string `json:"inAppOwnershipType"`
	PurchaseDate          int64  `json:"purchaseDate"`
	ExpiresDate           int64  `json:"expiresDate"`
	SignedDate            int64  `json:"signedDate"`
	RevocationDate        int64  `json:"revocationDate"`
	Price                 int64  `json:"price"`
	Currency              string `json:"currency"`
	OfferIdentifier       string `json:"offerIdentifier"`
	OfferType             int    `json:"offerType"`
	IsUpgraded            bool   `json:"isUpgraded"`
}
type AppleRenewal struct {
	OriginalTransactionID  string `json:"originalTransactionId"`
	AutoRenewProductID     string `json:"autoRenewProductId"`
	AutoRenewStatus        int    `json:"autoRenewStatus"`
	GracePeriodExpiresDate int64  `json:"gracePeriodExpiresDate"`
	SignedDate             int64  `json:"signedDate"`
	Environment            string `json:"environment"`
	AppAccountToken        string `json:"appAccountToken"`
}
type AppleNotification struct {
	NotificationUUID string `json:"notificationUUID"`
	NotificationType string `json:"notificationType"`
	Subtype          string `json:"subtype"`
	SignedDate       int64  `json:"signedDate"`
	Version          string `json:"version"`
	Data             struct {
		AppAppleID        int64  `json:"appAppleId"`
		BundleID          string `json:"bundleId"`
		Environment       string `json:"environment"`
		SignedTransaction string `json:"signedTransactionInfo"`
		SignedRenewal     string `json:"signedRenewalInfo"`
	} `json:"data"`
	Transaction *AppleTransaction `json:"-"`
	Renewal     *AppleRenewal     `json:"-"`
}
type AppleSubscriptionStatus struct {
	Status      int
	Transaction AppleTransaction
	Renewal     AppleRenewal
}

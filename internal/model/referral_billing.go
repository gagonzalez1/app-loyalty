package model

import "time"

// BillingInvoice is the provider's authorized-payment resource. Amounts are ARS minor units.
type BillingInvoice struct {
	ID, SubscriptionID, PaymentID, Currency, PaymentStatus string
	AmountMinor                                            int64
	CreatedAt                                              time.Time
}

// BillingPayment is fetched independently so a refunded payment cannot accrue rewards.
type BillingPayment struct {
	ID, Status, Currency, ExternalReference string
	AmountMinor, RefundedMinor              int64
	ApprovedAt                              *time.Time
}

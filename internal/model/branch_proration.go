package model

import "time"

type BranchQuote struct {
	QuoteID                   string    `json:"quote_id"`
	ExpiresAt                 time.Time `json:"expires_at"`
	Currency                  string    `json:"currency"`
	CurrentMonthlyAmountCents int64     `json:"current_monthly_amount_cents"`
	NewMonthlyAmountCents     int64     `json:"new_monthly_amount_cents"`
	ProrationAmountCents      int64     `json:"proration_amount_cents"`
	UnitAmountCents           int64     `json:"unit_amount_cents"`
	RemainingDays             int       `json:"remaining_days"`
	CycleDays                 int       `json:"cycle_days"`
	PeriodStart               string    `json:"period_start"`
	PeriodEnd                 string    `json:"period_end"`
	PaymentRequired           bool      `json:"payment_required"`
	Simulation                bool      `json:"simulation"`
}
type BranchOperation struct {
	ID          string      `json:"id"`
	Simulated   bool        `json:"simulated"`
	Status      string      `json:"status"`
	Quote       BranchQuote `json:"quote"`
	Branch      *Branch     `json:"branch,omitempty"`
	CheckoutURL string      `json:"checkout_url,omitempty"`
	ErrorCode   string      `json:"error_code,omitempty"`
}
type BranchPaymentRequest struct {
	Reference, PayerEmail, BackURL, IdempotencyKey string
	AmountMinor                                    int64
	ExpiresAt                                      time.Time
}
type BranchPaymentCheckout struct{ ID, URL string }

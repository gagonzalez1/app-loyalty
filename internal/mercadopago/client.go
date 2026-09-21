package mercadopago

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clientesFrecuentes/internal/model"
)

type Client struct {
	baseURL, accessToken string
	http                 *http.Client
}

func New(baseURL, accessToken string, timeout time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), accessToken: accessToken, http: &http.Client{Timeout: timeout}}
}

type preapproval struct {
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	ExternalReference string     `json:"external_reference"`
	InitPoint         string     `json:"init_point"`
	NextPaymentDate   *time.Time `json:"next_payment_date"`
}

func (c *Client) CreateSubscription(ctx context.Context, in model.BillingSubscriptionRequest) (model.BillingSubscriptionResult, error) {
	autoRecurring := map[string]any{"frequency": 1, "frequency_type": "months", "transaction_amount": in.Amount, "currency_id": in.Currency}
	if in.FreeTrialMonths > 0 {
		autoRecurring["free_trial"] = map[string]any{"frequency": in.FreeTrialMonths, "frequency_type": "months"}
	}
	body := map[string]any{
		"reason": in.Reason, "external_reference": in.ExternalReference, "payer_email": in.PayerEmail,
		"auto_recurring": autoRecurring,
		"back_url":       in.BackURL, "status": "pending",
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return model.BillingSubscriptionResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/preapproval", bytes.NewReader(encoded))
	if err != nil {
		return model.BillingSubscriptionResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", in.IdempotencyKey)
	return c.do(req)
}

func (c *Client) GetSubscription(ctx context.Context, id string) (model.BillingSubscriptionResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/preapproval/"+id, nil)
	if err != nil {
		return model.BillingSubscriptionResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	return c.do(req)
}

func (c *Client) CancelSubscription(ctx context.Context, id, idempotencyKey string) (model.BillingSubscriptionResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/preapproval/"+id, strings.NewReader(`{"status":"canceled"}`))
	if err != nil {
		return model.BillingSubscriptionResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", idempotencyKey)
	return c.do(req)
}

func (c *Client) do(req *http.Request) (model.BillingSubscriptionResult, error) {
	response, err := c.http.Do(req)
	if err != nil {
		return model.BillingSubscriptionResult{}, err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 1<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, limited)
		return model.BillingSubscriptionResult{}, fmt.Errorf("mercado pago returned %d", response.StatusCode)
	}
	var out preapproval
	if err = json.NewDecoder(limited).Decode(&out); err != nil {
		return model.BillingSubscriptionResult{}, err
	}
	if out.ID == "" || out.ExternalReference == "" {
		return model.BillingSubscriptionResult{}, errors.New("mercado pago returned an incomplete subscription")
	}
	return model.BillingSubscriptionResult{ID: out.ID, Status: out.Status, ExternalReference: out.ExternalReference, CheckoutURL: out.InitPoint, NextPaymentDate: out.NextPaymentDate}, nil
}

func ValidateSignature(header, requestID, resourceID, secret string, now time.Time) bool {
	parts := map[string]string{}
	for _, raw := range strings.Split(header, ",") {
		pair := strings.SplitN(strings.TrimSpace(raw), "=", 2)
		if len(pair) == 2 {
			parts[pair[0]] = pair[1]
		}
	}
	ts, signature := parts["ts"], parts["v1"]
	provided, err := hex.DecodeString(signature)
	if err != nil || ts == "" || requestID == "" || resourceID == "" || secret == "" || len(provided) != sha256.Size {
		return false
	}
	seconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || seconds < now.Add(-5*time.Minute).Unix() || seconds > now.Add(time.Minute).Unix() {
		return false
	}
	manifest := "id:" + strings.ToLower(resourceID) + ";request-id:" + requestID + ";ts:" + ts + ";"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(manifest))
	return subtle.ConstantTimeCompare(provided, mac.Sum(nil)) == 1
}

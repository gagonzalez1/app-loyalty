package mercadopago

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"clientesFrecuentes/internal/model"
)

func TestValidateSignature(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	resourceID, requestID, secret := "abc-123", "req-9", "webhook-secret"
	ts := "1800000000"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("id:abc-123;request-id:req-9;ts:" + ts + ";"))
	header := "ts=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	if !ValidateSignature(header, requestID, resourceID, secret, now) {
		t.Fatal("valid signature rejected")
	}
	if ValidateSignature(header, requestID, "different", secret, now) {
		t.Fatal("signature accepted for different resource")
	}
	if ValidateSignature(header, requestID, resourceID, secret, now.Add(10*time.Minute)) {
		t.Fatal("stale signature accepted")
	}
}

func TestCreateSubscriptionIsMonthlyAndServerSide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/preapproval" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("missing private authorization")
		}
		if r.Header.Get("X-Idempotency-Key") != "idem-1" {
			t.Fatal("missing provider idempotency key")
		}
		var body struct {
			AutoRecurring struct {
				Frequency     int     `json:"frequency"`
				FrequencyType string  `json:"frequency_type"`
				Amount        float64 `json:"transaction_amount"`
				Currency      string  `json:"currency_id"`
				FreeTrial     struct {
					Frequency     int    `json:"frequency"`
					FrequencyType string `json:"frequency_type"`
				} `json:"free_trial"`
			} `json:"auto_recurring"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.AutoRecurring.Frequency != 1 || body.AutoRecurring.FrequencyType != "months" || body.AutoRecurring.Amount != 30000 || body.AutoRecurring.Currency != "ARS" {
			t.Fatalf("unexpected recurring payload: %+v", body.AutoRecurring)
		}
		if body.AutoRecurring.FreeTrial.Frequency != 1 || body.AutoRecurring.FreeTrial.FrequencyType != "months" {
			t.Fatalf("unexpected free trial payload: %+v", body.AutoRecurring.FreeTrial)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"preapproval-1","status":"pending","external_reference":"puntazo:brand:1:key","init_point":"https://www.mercadopago.com.ar/subscriptions/checkout"}`))
	}))
	defer server.Close()

	client := New(server.URL, "private-token", time.Second)
	out, err := client.CreateSubscription(t.Context(), model.BillingSubscriptionRequest{Reason: "Puntazo", ExternalReference: "puntazo:brand:1:key", PayerEmail: "owner@example.test", BackURL: "https://puntazo.pro/suscripcion/resultado", IdempotencyKey: "idem-1", Currency: "ARS", Amount: 30000, FreeTrialMonths: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "preapproval-1" || out.Status != "pending" || out.CheckoutURL == "" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestCreateSubscriptionOmitsFreeTrialWhenItWasAlreadyUsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AutoRecurring map[string]any `json:"auto_recurring"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, present := body.AutoRecurring["free_trial"]; present {
			t.Fatal("free_trial must be omitted after the first subscription")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"preapproval-2","status":"pending","external_reference":"puntazo:brand:1:second","init_point":"https://www.mercadopago.com.ar/subscriptions/checkout"}`))
	}))
	defer server.Close()

	client := New(server.URL, "private-token", time.Second)
	_, err := client.CreateSubscription(t.Context(), model.BillingSubscriptionRequest{Reason: "Puntazo", ExternalReference: "puntazo:brand:1:second", PayerEmail: "owner@example.test", BackURL: "https://puntazo.pro/suscripcion/resultado", IdempotencyKey: "idem-2", Currency: "ARS", Amount: 30000})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCancelSubscriptionStopsProviderRenewal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/preapproval/preapproval-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("X-Idempotency-Key") != "cancel-1" {
			t.Fatal("missing private or idempotency header")
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Status != "canceled" {
			t.Fatalf("status = %q", body.Status)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"preapproval-1","status":"cancelled","external_reference":"puntazo:brand:1:key"}`))
	}))
	defer server.Close()

	client := New(server.URL, "private-token", time.Second)
	out, err := client.CancelSubscription(t.Context(), "preapproval-1", "cancel-1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "cancelled" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

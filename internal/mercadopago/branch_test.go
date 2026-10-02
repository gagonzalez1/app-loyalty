package mercadopago

import (
	"clientesFrecuentes/internal/model"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBranchCheckoutMinorUnitsAndDeadline(t *testing.T) {
	deadline := time.Date(2026, 10, 15, 16, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/checkout/preferences" || r.Header.Get("X-Idempotency-Key") != "key" {
			t.Error("wrongcheckoutrequest")
		}
		var body struct {
			Items []struct {
				Price json.Number `json:"unit_price"`
			} `json:"items"`
			Expires  bool      `json:"expires"`
			Deadline time.Time `json:"expiration_date_to"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Fatal(e)
		}
		if body.Items[0].Price.String() != "13709.68" || !body.Expires || !body.Deadline.Equal(deadline) {
			t.Errorf("checkoutbody %+v", body)
		}
		w.Write([]byte(`{"id":"pref","init_point":"https://checkout.example.test"}`))
	}))
	defer server.Close()
	client := New(server.URL, "fake", time.Second)
	out, e := client.CreateBranchCheckout(t.Context(), model.BranchPaymentRequest{Reference: "puntazo:branch:op", PayerEmail: "owner@example.test", BackURL: "https://app.example.test", IdempotencyKey: "key", AmountMinor: 1370968, ExpiresAt: deadline})
	if e != nil || out.ID != "pref" {
		t.Fatalf("out%+v err%v", out, e)
	}
}
func TestBranchPaymentSearchRejectThenApproved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/payments/search":
			w.Write([]byte(`{"results":[{"id":2,"external_reference":"puntazo:branch:op","status":"rejected"},{"id":1,"external_reference":"puntazo:branch:op","status":"approved"}]}`))
		case "/v1/payments/1":
			w.Write([]byte(`{"id":1,"external_reference":"puntazo:branch:op","status":"approved","currency_id":"ARS","transaction_amount":1.59,"transaction_amount_refunded":0,"date_approved":"2026-10-15T15:00:00Z"}`))
		default:
			t.Error("wrongpath", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(server.URL, "fake", time.Second)
	p, found, e := client.FindBranchPayment(t.Context(), "puntazo:branch:op")
	if e != nil || !found || p.ID != "1" || p.AmountMinor != 159 || p.ApprovedAt == nil {
		t.Fatalf("payment%+v found%v err%v", p, found, e)
	}
}

func TestBranchDuplicateApprovalRefundReadback(t *testing.T) {
	refunded := false
	refunds := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/payments/search":
			w.Write([]byte(`{"results":[{"id":2,"external_reference":"puntazo:branch:op","status":"approved"},{"id":1,"external_reference":"puntazo:branch:op","status":"approved"}]}`))
		case "/v1/payments/1/refunds":
			if r.Method != "POST" || r.Header.Get("X-Idempotency-Key") == "" {
				t.Error("refund missingkey")
			}
			refunds++
			refunded = true
			w.Write([]byte(`{"id":99}`))
		case "/v1/payments/1":
			if refunded {
				w.Write([]byte(`{"id":1,"external_reference":"puntazo:branch:op","status":"refunded","currency_id":"ARS","transaction_amount":1.59,"transaction_amount_refunded":1.59}`))
			} else {
				w.Write([]byte(`{"id":1,"external_reference":"puntazo:branch:op","status":"approved","currency_id":"ARS","transaction_amount":1.59,"transaction_amount_refunded":0}`))
			}
		case "/v1/payments/2":
			w.Write([]byte(`{"id":2,"external_reference":"puntazo:branch:op","status":"approved","currency_id":"ARS","transaction_amount":1.59,"transaction_amount_refunded":0}`))
		default:
			t.Error("unexpectedpath", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(server.URL, "fake", time.Second)
	for i := 0; i < 2; i++ {
		p, found, e := client.FindBranchPayment(t.Context(), "puntazo:branch:op")
		if e != nil || !found || p.ID != "2" {
			t.Fatalf("payment%+v found%v err%v", p, found, e)
		}
	}
	if refunds != 1 {
		t.Fatalf("refunds%d want1", refunds)
	}
}

func TestBranchLateDuplicateKeepsOriginalPayment(t *testing.T) {
	refunds := 0
	refunded := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/payments/search":
			w.Write([]byte(`{"results":[{"id":2,"external_reference":"puntazo:branch:op","status":"approved"},{"id":1,"external_reference":"puntazo:branch:op","status":"approved"}]}`))
		case "/v1/payments/2/refunds":
			refunds++
			refunded = true
			w.Write([]byte(`{"id":88}`))
		case "/v1/payments/2":
			if refunded {
				w.Write([]byte(`{"id":2,"external_reference":"puntazo:branch:op","status":"refunded","currency_id":"ARS","transaction_amount":1.59,"transaction_amount_refunded":1.59}`))
			} else {
				w.Write([]byte(`{"id":2,"external_reference":"puntazo:branch:op","status":"approved","currency_id":"ARS","transaction_amount":1.59,"transaction_amount_refunded":0}`))
			}
		default:
			t.Error("original payment must be retained", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(server.URL, "fake", time.Second)
	for i := 0; i < 2; i++ {
		if e := client.ReconcileBranchDuplicatePayments(t.Context(), "puntazo:branch:op", "1"); e != nil {
			t.Fatal(e)
		}
	}
	if refunds != 1 {
		t.Fatal("duplicate refund repeated", refunds)
	}
}

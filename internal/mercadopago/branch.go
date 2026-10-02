package mercadopago

import (
	"bytes"
	"clientesFrecuentes/internal/model"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"time"
)

func (c *Client) CreateBranchCheckout(ctx context.Context, in model.BranchPaymentRequest) (model.BranchPaymentCheckout, error) {
	if in.AmountMinor < 1 || in.Reference == "" || in.IdempotencyKey == "" {
		return model.BranchPaymentCheckout{}, errors.New("invalid branch payment")
	}
	body, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"id": in.Reference, "title": "Puntazo: días de la sucursal nueva", "quantity": 1, "currency_id": "ARS", "unit_price": minorJSON(in.AmountMinor)}}, "external_reference": in.Reference, "payer": map[string]any{"email": in.PayerEmail}, "back_urls": map[string]string{"success": in.BackURL, "pending": in.BackURL, "failure": in.BackURL}, "expires": true, "expiration_date_to": in.ExpiresAt.Format(time.RFC3339)})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/checkout/preferences", bytes.NewReader(body))
	if e != nil {
		return model.BranchPaymentCheckout{}, e
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", in.IdempotencyKey)
	resp, e := c.http.Do(req)
	if e != nil {
		return model.BranchPaymentCheckout{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return model.BranchPaymentCheckout{}, &RequestError{Status: resp.StatusCode}
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"init_point"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if e == nil && (out.ID == "" || out.URL == "") {
		e = errors.New("incomplete checkout")
	}
	return model.BranchPaymentCheckout{ID: out.ID, URL: out.URL}, e
}
func (c *Client) FindBranchPayment(ctx context.Context, reference string) (model.BillingPayment, bool, error) {
	var out struct {
		Results []struct {
			ID        json.Number `json:"id"`
			Reference string      `json:"external_reference"`
			Status    string      `json:"status"`
		} `json:"results"`
	}
	e := c.getJSON(ctx, "/v1/payments/search?"+url.Values{"external_reference": {reference}, "sort": {"date_created"}, "criteria": {"desc"}, "limit": {"100"}}.Encode(), &out)
	if e != nil {
		return model.BillingPayment{}, false, e
	}
	var id string
	approved := []string{}
	for _, item := range out.Results {
		if item.Reference != reference {
			continue
		}
		if item.Status == "approved" {
			approved = append(approved, item.ID.String())
		} else if id == "" {
			id = item.ID.String()
		}
	}
	if len(approved) > 0 {
		id = approved[0]
	}
	// A Checkout Pro preference can be paid twice. Keep one verified payment;
	// reimburse every additional approved payment before activating the branch.
	// The provider refund has an independent deterministic key and readback, so
	// network uncertainty and process restart cannot double refund it.
	extras := []string{}
	if len(approved) > 1 {
		extras = approved[1:]
	}
	for _, extra := range extras {
		p, e := c.GetPayment(ctx, extra)
		if e != nil {
			return model.BillingPayment{}, false, e
		}
		if p.ExternalReference != reference {
			return model.BillingPayment{}, false, errors.New("duplicate payment reference mismatch")
		}
		if e = c.RefundBranchPayment(ctx, extra, uuid.NewSHA1(uuid.NameSpaceURL, []byte(reference+":duplicate:"+extra)).String()); e != nil {
			return model.BillingPayment{}, false, e
		}
		verified, e := c.GetPayment(ctx, extra)
		if e != nil || verified.RefundedMinor < verified.AmountMinor {
			return model.BillingPayment{}, false, errors.New("duplicate payment refund not confirmed")
		}
	}

	if id == "" {
		return model.BillingPayment{}, false, nil
	}
	p, e := c.GetPayment(ctx, id)
	return p, true, e
}
func (c *Client) RefundBranchPayment(ctx context.Context, id, key string) error {
	if !numericID(id) || key == "" {
		return errors.New("invalid refund")
	}
	// Read verified payment first so a repeated refund never exceeds collected funds.
	p, e := c.GetPayment(ctx, id)
	if e != nil {
		return e
	}
	if p.RefundedMinor >= p.AmountMinor && p.AmountMinor > 0 {
		return nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/payments/"+id+"/refunds", bytes.NewBufferString("{}"))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", key)
	resp, e := c.http.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &RequestError{Status: resp.StatusCode}
	}
	return nil
}

// ReconcileBranchDuplicatePayments retains the original operation payment even
// when another approval arrives after branch activation. Webhooks and a durable
// 30-day completed-operation poll both invoke it.
func (c *Client) ReconcileBranchDuplicatePayments(ctx context.Context, reference, primary string) error {
	var out struct {
		Results []struct {
			ID        json.Number `json:"id"`
			Reference string      `json:"external_reference"`
			Status    string      `json:"status"`
		} `json:"results"`
	}
	if e := c.getJSON(ctx, "/v1/payments/search?"+url.Values{"external_reference": {reference}, "limit": {"100"}}.Encode(), &out); e != nil {
		return e
	}
	for _, item := range out.Results {
		id := item.ID.String()
		if item.Reference != reference || id == primary || item.Status != "approved" {
			continue
		}
		payment, e := c.GetPayment(ctx, id)
		if e != nil {
			return e
		}
		if payment.ExternalReference != reference {
			return errors.New("duplicate payment reference mismatch")
		}
		if e = c.RefundBranchPayment(ctx, id, uuid.NewSHA1(uuid.NameSpaceURL, []byte(reference+":duplicate:"+id)).String()); e != nil {
			return e
		}
		verified, e := c.GetPayment(ctx, id)
		if e != nil {
			return e
		}
		if verified.RefundedMinor < verified.AmountMinor {
			return errors.New("duplicate payment refund not confirmed")
		}
	}
	return nil
}

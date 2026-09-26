// Package push sends card refresh notifications through the Expo Push Service.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const Endpoint = "https://exp.host/--/api/v2/push/send"

var ErrDeviceNotRegistered = errors.New("expo push token is no longer registered")

// DeliveryError reports an Expo rejection without exposing the token or Expo's
// free-form message, either of which could contain private data.
type DeliveryError struct {
	StatusCode int
	Code       string
}

func (e *DeliveryError) Error() string {
	if e.Code != "" {
		return "expo push rejected notification: " + e.Code
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("expo push request failed with HTTP %d", e.StatusCode)
	}
	return "expo push rejected notification"
}

func (e *DeliveryError) Is(target error) bool {
	return target == ErrDeviceNotRegistered && e.Code == "DeviceNotRegistered"
}

type Client struct {
	http     *http.Client
	endpoint string
}

func NewClient(httpClient *http.Client) *Client {
	return NewClientWithURL(httpClient, Endpoint)
}

// NewClientWithURL permits an alternate endpoint for local tests.
func NewClientWithURL(httpClient *http.Client, endpoint string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{http: httpClient, endpoint: endpoint}
}

type notification struct {
	To    string `json:"to"`
	Sound string `json:"sound"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Data  struct {
		Action string `json:"action"`
	} `json:"data"`
}

type ticket struct {
	Status  string `json:"status"`
	ID      string `json:"id"`
	Details struct {
		Error string `json:"error"`
	} `json:"details"`
}

type apiError struct {
	Code string `json:"code"`
}

type response struct {
	Data   json.RawMessage `json:"data"`
	Errors []apiError      `json:"errors"`
}

// Send asks Expo to deliver a card refresh notification. Success means Expo
// accepted the message, not that the device received it.
func (c *Client) Send(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("expo push token is empty")
	}
	message := notification{
		To:    token,
		Sound: "default",
		Title: "Tarjetas actualizadas",
		Body:  "Tus tarjetas se han actualizado.",
	}
	message.Data.Action = "REFRESH_CARDS"
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode expo push notification: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create expo push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send expo push request: %w", err)
	}
	defer res.Body.Close()
	var result response
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&result); err != nil {
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return &DeliveryError{StatusCode: res.StatusCode}
		}
		return fmt.Errorf("decode expo push response: %w", err)
	}
	if len(result.Errors) > 0 {
		return &DeliveryError{StatusCode: res.StatusCode, Code: safeErrorCode(result.Errors[0].Code)}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &DeliveryError{StatusCode: res.StatusCode}
	}
	var t ticket
	if err := json.Unmarshal(result.Data, &t); err != nil {
		return fmt.Errorf("decode expo push ticket: %w", err)
	}
	switch t.Status {
	case "ok":
		return nil
	case "error":
		return &DeliveryError{Code: safeErrorCode(t.Details.Error)}
	default:
		return errors.New("expo push response has no valid ticket status")
	}
}

func safeErrorCode(code string) string {
	if len(code) > 64 || code == "" {
		return ""
	}
	for _, r := range code {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_", r) {
			return ""
		}
	}
	return code
}

package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendCardRefreshPayload(t *testing.T) {
	const token = "ExpoPushToken[private-token]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/--/api/v2/push/send" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		want := map[string]any{
			"to": token, "sound": "default", "title": "Tarjetas actualizadas",
			"body": "Tus tarjetas se han actualizado.",
			"data": map[string]any{"action": "REFRESH_CARDS"},
		}
		got, _ := json.Marshal(body)
		expected, _ := json.Marshal(want)
		if string(got) != string(expected) {
			t.Errorf("payload = %s; want %s", got, expected)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"status":"ok","id":"ticket-id"}}`)
	}))
	defer server.Close()

	c := NewClientWithURL(server.Client(), server.URL+"/--/api/v2/push/send")
	if err := c.Send(context.Background(), token); err != nil {
		t.Fatal(err)
	}
}

func TestSendExpoErrorsDoNotExposeToken(t *testing.T) {
	const token = "ExpoPushToken[secret]"
	tests := []struct {
		name         string
		status       int
		body         string
		unregistered bool
	}{
		{"ticket unregistered", 200, `{"data":{"status":"error","message":"ExpoPushToken[secret] invalid","details":{"error":"DeviceNotRegistered"}}}`, true},
		{"ticket rejected", 200, `{"data":{"status":"error","message":"ExpoPushToken[secret] invalid","details":{"error":"MessageTooBig"}}}`, false},
		{"request rejected", 400, `{"errors":[{"code":"PUSH_TOO_MANY_EXPERIENCE_IDS","message":"ExpoPushToken[secret]"}]}`, false},
		{"server unavailable", 503, `unavailable ExpoPushToken[secret]`, false},
		{"malformed success", 200, `{"data":{"status":"unknown"}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := NewClientWithURL(server.Client(), server.URL).Send(context.Background(), token)
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, ErrDeviceNotRegistered) != tc.unregistered {
				t.Errorf("unregistered = %v; error = %v", errors.Is(err, ErrDeviceNotRegistered), err)
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaks token: %v", err)
			}
		})
	}
}

func TestSendHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewClientWithURL(nil, "http://127.0.0.1:1").Send(ctx, "ExpoPushToken[valid]")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context.Canceled", err)
	}
}

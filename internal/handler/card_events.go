package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"clientesFrecuentes/internal/cardevents"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

const cardEventFrame = "event: cards\ndata: {\"action\":\"REFRESH_CARDS\"}\n\n"

func (h *Handler) CardEventsStream(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "CLIENTE_FINAL" {
		writeErr(c, service.ErrForbidden)
		return
	}
	if h.CardEvents == nil || h.Tokens == nil {
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Eventos no disponibles", nil)
		return
	}

	// Register before reading the revision so a confirmation committed during
	// the initial query remains queued for this subscriber.
	events, down, unsubscribe, err := h.CardEvents.Subscribe(a.ID)
	if err != nil {
		if errors.Is(err, cardevents.ErrCapacity) {
			c.Header("Retry-After", "5")
			web.Error(c, http.StatusTooManyRequests, "RATE_LIMITED", "Demasiadas conexiones de eventos", nil)
			return
		}
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Eventos no disponibles", nil)
		return
	}
	defer unsubscribe()
	revision, err := h.cardRevision(c.Request.Context(), a.ID)
	if err != nil {
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Eventos no disponibles", nil)
		return
	}

	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Eventos no disponibles", nil)
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store, private")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)

	lastSent := c.GetHeader("Last-Event-ID")
	if lastSent != revision {
		if err := writeCardEvent(c, controller, revision); err != nil {
			return
		}
		lastSent = revision
	} else if err := writeSSE(c, controller, ": connected\n\n"); err != nil {
		return
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	// Close while the short-lived access token is still valid; the client
	// reconnects with its current token and can refresh on a later 401.
	maxLifetime := time.NewTimer(10 * time.Minute)
	defer maxLifetime.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-down:
			return
		case <-maxLifetime.C:
			return
		case <-heartbeat.C:
			if !h.cardStreamSessionActive(c, a.ID, a.SessionID) {
				return
			}
			if err := writeSSE(c, controller, ": heartbeat\n\n"); err != nil {
				return
			}
		case <-events:
			if !h.cardStreamSessionActive(c, a.ID, a.SessionID) {
				return
			}
			current, err := h.cardRevision(c.Request.Context(), a.ID)
			if err != nil {
				return
			}
			if current != lastSent {
				if err := writeCardEvent(c, controller, current); err != nil {
					return
				}
				lastSent = current
			}
		}
	}
}

func (h *Handler) cardRevision(parent context.Context, customerID int64) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	return h.Repo.CardRevision(ctx, customerID)
}

func (h *Handler) cardStreamSessionActive(c *gin.Context, customerID int64, sessionID string) bool {
	raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	id, _, currentSession, authVersion, _, err := h.Tokens.ParseSessionContext(raw)
	if err != nil || id != customerID || currentSession != sessionID {
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	accountType, err := h.Repo.ActiveSessionAccountType(ctx, customerID, sessionID, authVersion)
	return err == nil && accountType == "CLIENTE_FINAL"
}

func writeCardEvent(c *gin.Context, controller *http.ResponseController, revision string) error {
	return writeSSE(c, controller, "id: "+revision+"\n"+cardEventFrame)
}

func writeSSE(c *gin.Context, controller *http.ResponseController, frame string) error {
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := c.Writer.WriteString(frame); err != nil {
		return err
	}
	if err := controller.Flush(); err != nil {
		return err
	}
	return controller.SetWriteDeadline(time.Time{})
}

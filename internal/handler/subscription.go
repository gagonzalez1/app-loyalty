package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"clientesFrecuentes/internal/config"
	"clientesFrecuentes/internal/mercadopago"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Subscription(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	out, err := h.Service.Subscription(c.Request.Context(), a.ID, brandID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.Subscription]{Data: out, RequestID: web.RequestID(c)})
}

func (h *Handler) CreateSubscriptionCheckout(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	out, err := h.Service.CreateSubscriptionCheckout(c.Request.Context(), a.ID, brandID, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, web.Envelope[model.Subscription]{Data: out, RequestID: web.RequestID(c)})
}

func (h *Handler) CancelSubscription(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	out, err := h.Service.CancelSubscription(c.Request.Context(), a.ID, brandID, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.Subscription]{Data: out, RequestID: web.RequestID(c)})
}

type mercadoPagoWebhook struct {
	ID   json.RawMessage `json:"id"`
	Type string          `json:"type"`
	Data struct {
		ID json.RawMessage `json:"id"`
	} `json:"data"`
}

func rawIdentifier(value json.RawMessage) string {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return ""
	}
	var text string
	if trimmed[0] == '"' && json.Unmarshal(trimmed, &text) == nil {
		return text
	}
	return string(trimmed)
}

func (h *Handler) MercadoPagoWebhook(c *gin.Context) {
	if h.Service.Config.MercadoPagoProvider != "api" {
		c.Status(http.StatusNoContent)
		return
	}
	var payload mercadoPagoWebhook
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, config.MaxJSONBytes)
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&payload); err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	resourceID := strings.TrimSpace(c.Query("data.id"))
	bodyResourceID := rawIdentifier(payload.Data.ID)
	if resourceID == "" {
		resourceID = bodyResourceID
	}
	if bodyResourceID != "" && resourceID != bodyResourceID {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !mercadopago.ValidateSignature(c.GetHeader("x-signature"), c.GetHeader("x-request-id"), resourceID, h.Service.Config.MercadoPagoWebhookSecret, h.Service.Now()) {
		web.Error(c, http.StatusUnauthorized, "INVALID_WEBHOOK_SIGNATURE", "Firma de webhook inválida", nil)
		return
	}
	if payload.Type != "subscription_preapproval" {
		c.Status(http.StatusNoContent)
		return
	}
	notificationID := rawIdentifier(payload.ID)
	if notificationID == "" {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if err := h.Service.ApplySubscriptionWebhook(c.Request.Context(), notificationID, payload.Type, resourceID); err != nil {
		writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

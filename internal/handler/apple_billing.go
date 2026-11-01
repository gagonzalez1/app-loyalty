package handler

import (
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"time"
)

func appleBrandActor(c *gin.Context) (int64, int64, bool) {
	a, ok := actor(c)
	if !ok {
		return 0, 0, false
	}
	brand, e := positiveID(c.Param("brand_id"))
	if e != nil {
		writeErr(c, e)
		return 0, 0, false
	}
	return a.ID, brand, true
}
func (h *Handler) BillingState(c *gin.Context) {
	a, b, ok := appleBrandActor(c)
	if !ok {
		return
	}
	out, e := h.Service.BrandBilling(c.Request.Context(), a, b)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.BrandBillingState]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) AppleCatalog(c *gin.Context) {
	a, b, ok := appleBrandActor(c)
	if !ok {
		return
	}
	out, e := h.Service.AppleBillingCatalog(c.Request.Context(), a, b)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.AppleCatalog]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) CreateAppleIntent(c *gin.Context) {
	a, b, ok := appleBrandActor(c)
	if !ok {
		return
	}
	if !h.limit(c, "apple-intent:ip:"+h.clientIP(c), 30, time.Minute) {
		return
	}
	var req model.ApplePurchaseIntentRequest
	if decode(c, &req) != nil {
		web.Error(c, 400, "INVALID_REQUEST", "Solicitud inválida", nil)
		return
	}
	out, e := h.Service.CreateAppleBillingIntent(c.Request.Context(), a, b, c.GetHeader("Idempotency-Key"), req)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(http.StatusCreated, web.Envelope[model.ApplePurchaseIntent]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) SyncAppleBilling(c *gin.Context) {
	a, b, ok := appleBrandActor(c)
	if !ok {
		return
	}
	if !h.limit(c, "apple-sync:ip:"+h.clientIP(c), 30, time.Minute) {
		return
	}
	var req model.AppleSyncRequest
	if decode(c, &req) != nil {
		web.Error(c, 400, "INVALID_REQUEST", "Solicitud inválida", nil)
		return
	}
	out, e := h.Service.SyncApplePurchase(c.Request.Context(), a, b, req)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.BrandBillingState]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) AppleAppStoreNotification(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 768*1024)
	var req struct {
		SignedPayload string `json:"signedPayload"`
	}
	if decode(c, &req) != nil {
		web.Error(c, 400, "INVALID_REQUEST", "Notificación inválida", nil)
		return
	}
	if e := h.Service.ReceiveAppleNotification(c.Request.Context(), req.SignedPayload); e != nil {
		writeErr(c, e)
		return
	}
	c.Status(http.StatusNoContent)
}

// Attach to authenticated brand routes. Billing, privacy and deactivation remain
// reachable to resolve restrictions; movement operations also guard their DB transaction.
func (h *Handler) RequireAppleBillingWrite(c *gin.Context) {
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
		c.Next()
		return
	}
	path := c.FullPath()
	if strings.Contains(path, "/billing") || strings.Contains(path, "/suscripcion") || (c.Request.Method == http.MethodDelete && (strings.HasSuffix(path, "/sucursales/:resource_id") || strings.HasSuffix(path, "/marcas/:brand_id"))) {
		c.Next()
		return
	}
	raw := c.Param("brand_id")
	if raw == "" {
		c.Next()
		return
	}
	a, b, ok := appleBrandActor(c)
	if !ok {
		c.Abort()
		return
	}
	if e := h.Service.Repo.CheckAppleBillingWrite(c.Request.Context(), a, b); e != nil {
		writeErr(c, e)
		c.Abort()
		return
	}
	c.Next()
}

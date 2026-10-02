package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func (h *Handler) RegisterDemoMerchant(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.RegisterDemoMerchantRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "merchant-register:ip:"+h.clientIP(c), merchantIPAttempts, merchantIPWindow) || !h.limit(c, "merchant-register:email:"+strings.ToLower(strings.TrimSpace(req.Email)), merchantEmailAttempts, merchantEmailWindow) {
		return
	}
	result, err := h.Service.RegisterDemoMerchant(c.Request.Context(), c.GetHeader("Idempotency-Key"), web.RequestID(c), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	if result.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	var envelope web.Envelope[model.DemoMerchantData]
	if err = json.Unmarshal(result.Body, &envelope); err != nil {
		writeErr(c, err)
		return
	}
	if platform == "web" {
		if envelope.Data.Session != nil {
			setRefreshCookie(c, envelope.Data.Session.RefreshToken)
			envelope.Data.Session.RefreshToken = ""
		}
	}
	result.Body, err = json.Marshal(envelope)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Data(result.Status, "application/json", result.Body)
}

func (h *Handler) ListBrands(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	data, err := h.Service.ListBrands(c.Request.Context(), a.ID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[[]model.MerchantContext]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) Brand(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	id, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.Brand(c.Request.Context(), a.ID, id)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("ETag", accountETag(data.BrandVersion))
	c.JSON(http.StatusOK, web.Envelope[model.MerchantContext]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) Benefits(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	data, err := h.Service.Benefits(c.Request.Context(), a.ID, brandID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[[]model.Benefit]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) CreateBenefit(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	var req model.CreateBenefitRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.CreateBenefit(c.Request.Context(), a.ID, brandID, req)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("ETag", `"1"`)
	c.JSON(http.StatusCreated, web.Envelope[model.Benefit]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) BrandMovements(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	page, size, err := pagination(c)
	if err != nil {
		writeErr(c, err)
		return
	}
	data, p, err := h.Service.BrandMovements(c.Request.Context(), a.ID, brandID, page, size)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.PageEnvelope[model.Movement]{Data: data, Pagination: p, RequestID: web.RequestID(c)})
}

func (h *Handler) BrandCustomers(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	page, size, err := pagination(c)
	if err != nil {
		writeErr(c, err)
		return
	}
	data, p, err := h.Service.BrandCustomers(c.Request.Context(), a.ID, brandID, page, size, c.Query("search"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.PageEnvelope[model.BrandCustomer]{Data: data, Pagination: p, RequestID: web.RequestID(c)})
}

func (h *Handler) BrandMetrics(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	data, err := h.Service.BrandMetrics(c.Request.Context(), a.ID, brandID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.BrandMetricsSummary]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) BrandPeriodMetrics(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return
	}
	brandID, err := positiveID(c.Param("brand_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	data, err := h.Service.BrandPeriodMetrics(c.Request.Context(), a.ID, brandID, c.Query("period"), c.Query("date"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.BrandPeriodMetrics]{Data: data, RequestID: web.RequestID(c)})
}

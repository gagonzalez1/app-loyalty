package handler

import (
	"net/http"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func (h *Handler) CreateGiftCardBatch(c *gin.Context) {
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
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	var req model.GiftCardCreateRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.CreateGiftCardBatch(c.Request.Context(), a.ID, brandID, req)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, web.Envelope[model.GiftCardBatch]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) GiftCardBatches(c *gin.Context) {
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
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.ListGiftCardBatches(c.Request.Context(), a.ID, brandID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[[]model.GiftCardBatch]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) RevokeGiftCardBatch(c *gin.Context) {
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
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if err = h.Service.RevokeGiftCardBatch(c.Request.Context(), a.ID, brandID, c.Param("batch_id")); err != nil {
		writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) PublicGiftCardClaim(c *gin.Context) {
	data, err := h.Service.GiftCardClaim(c.Request.Context(), c.Param("token"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.GiftCardClaim]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) RedeemGiftCard(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "CLIENTE_FINAL" {
		writeErr(c, service.ErrForbidden)
		return
	}
	var req model.GiftCardRedeemRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.RedeemGiftCard(c.Request.Context(), a.ID, req.Token)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, web.Envelope[model.GiftCardRedemption]{Data: data, RequestID: web.RequestID(c)})
}

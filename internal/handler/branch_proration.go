package handler

import (
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) QuoteBranch(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brand, e := positiveID(c.Param("brand_id"))
	if e != nil {
		writeErr(c, e)
		return
	}
	var in model.CreateBranchRequest
	if decode(c, &in) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	out, e := h.Service.QuoteBranch(c.Request.Context(), a.ID, brand, in)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, web.Envelope[model.BranchQuote]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) ConfirmBranch(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brand, e := positiveID(c.Param("brand_id"))
	if e != nil {
		writeErr(c, e)
		return
	}
	var in struct {
		QuoteID string `json:"quote_id"`
	}
	if decode(c, &in) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	out, e := h.Service.ConfirmBranch(c.Request.Context(), a.ID, brand, in.QuoteID, c.GetHeader("Idempotency-Key"))
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, web.Envelope[model.BranchOperation]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) BranchOperation(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brand, e := positiveID(c.Param("brand_id"))
	if e != nil {
		writeErr(c, e)
		return
	}
	out, e := h.Service.BranchOperation(c.Request.Context(), a.ID, brand, c.Param("operation_id"))
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, web.Envelope[model.BranchOperation]{Data: out, RequestID: web.RequestID(c)})
}
func (h *Handler) SimulateBranch(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	brand, e := positiveID(c.Param("brand_id"))
	if e != nil {
		writeErr(c, e)
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if decode(c, &in) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	out, e := h.Service.SimulateBranch(c.Request.Context(), a.ID, brand, c.Param("operation_id"), in.Action)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, web.Envelope[model.BranchOperation]{Data: out, RequestID: web.RequestID(c)})
}

package handler

import (
	"net/http"

	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func (h *Handler) SavePushToken(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "CLIENTE_FINAL" {
		writeErr(c, service.ErrForbidden)
		return
	}
	var req struct {
		ExpoPushToken string `json:"expo_push_token"`
		DeviceID      string `json:"device_id"`
	}
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if err := h.Service.SavePushToken(c.Request.Context(), a.ID, req.ExpoPushToken, req.DeviceID); err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[map[string]bool]{Data: map[string]bool{"registered": true}, RequestID: web.RequestID(c)})
}

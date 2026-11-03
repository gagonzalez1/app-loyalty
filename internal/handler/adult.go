package handler

import (
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) ConfirmAdult(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	var req struct {
		Confirmed bool `json:"confirmed"`
	}
	if decode(c, &req) != nil || !req.Confirmed {
		web.Error(c, http.StatusUnprocessableEntity, "ADULT_CONFIRMATION_REQUIRED", "Confirmá que tenés 18 años o más", nil)
		return
	}
	if err := h.Repo.ConfirmAdult(c.Request.Context(), a.ID); err != nil {
		writeErr(c, err)
		return
	}
	h.Me(c)
}

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

func (h *Handler) RequireAdult(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		c.Abort()
		return
	}
	confirmed, err := h.Repo.AdultConfirmed(c.Request.Context(), a.ID)
	if err != nil {
		web.AbortError(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "No se pudo verificar la confirmación de edad", nil)
		return
	}
	if !confirmed {
		web.AbortError(c, http.StatusForbidden, "ADULT_CONFIRMATION_REQUIRED", "Puntazo es para personas de 18 años o más", nil)
		return
	}
	c.Next()
}

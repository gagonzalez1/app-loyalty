package handler

import (
	"net/http"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) PublicStore(c *gin.Context) {
	branch, err := positiveID(c.Param("branch_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	if !h.limit(c, "public-store:ip:"+h.clientIP(c), 120, time.Minute) {
		return
	}
	store, err := h.Repo.PublicStore(c.Request.Context(), branch)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.PublicStore]{Data: store, RequestID: web.RequestID(c)})
}

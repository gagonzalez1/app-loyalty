package handler

import (
	"net/http"
	"strconv"
	"time"

	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func (h *Handler) RecordAppOpening(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if decode(c, &req) != nil || req.UserID <= 0 {
		writeErr(c, repository.ErrInvalidRequest)
		return
	}
	// The expected identity prevents an automatic retry from attributing an
	// opening to another account after a concurrent login.
	if req.UserID != a.ID {
		web.Error(c, http.StatusForbidden, "FORBIDDEN", "La sesión cambió", nil)
		return
	}
	if !h.limit(c, "app-open:user:"+strconv.FormatInt(a.ID, 10), 60, time.Minute) {
		return
	}
	if err := h.Repo.RecordAppOpening(c.Request.Context(), a.ID, a.AuthVersion); err != nil {
		writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

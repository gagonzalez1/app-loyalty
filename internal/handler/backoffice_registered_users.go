package handler

import (
	"net/http"
	"strconv"

	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) BackofficeRegisteredUsers(c *gin.Context) {
	if !requireSystemAdmin(c) {
		return
	}
	limit := 50
	if raw, supplied := c.GetQuery("limit"); supplied {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeErr(c, repository.ErrInvalidRequest)
			return
		}
		limit = value
	}
	var before int64
	if raw, supplied := c.GetQuery("before"); supplied {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			writeErr(c, repository.ErrInvalidRequest)
			return
		}
		before = value
	}
	out, err := h.Repo.ListBackofficeRegisteredUsers(c.Request.Context(), c.Query("q"), c.Query("account_type"), limit, before)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[repository.BackofficeRegisteredUsersPage]{Data: out, RequestID: web.RequestID(c)})
}

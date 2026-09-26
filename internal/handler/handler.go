package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/cardevents"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	Service           *service.Service
	Repo              *repository.Repository
	Tokens            *auth.Tokens
	CardEvents        *cardevents.Broker
	Limiter           *middleware.RateLimiter
	Uploads           *middleware.UploadSemaphore
	Logger            *slog.Logger
	TrustedProxyCount int
}

func actor(c *gin.Context) (middleware.Actor, bool) {
	a, ok := middleware.CurrentActor(c)
	if !ok {
		web.Error(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sesión inválida", nil)
	}
	return a, ok
}

func (h *Handler) limit(c *gin.Context, key string, n int, w time.Duration) bool {
	ok, retry, err := h.Limiter.Allow(c.Request.Context(), key, n, w)
	if err != nil {
		if h.Logger != nil {
			h.Logger.ErrorContext(c.Request.Context(), "rate limiter unavailable", "error", err)
		}
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Control de tráfico no disponible", nil)
		return false
	}
	if ok {
		return true
	}
	c.Header("Retry-After", strconv.Itoa(retry))
	web.Error(c, http.StatusTooManyRequests, "RATE_LIMITED", "Demasiados intentos", nil)
	return false
}

func (h *Handler) clientIP(c *gin.Context) string { return middleware.ClientIP(c, h.TrustedProxyCount) }

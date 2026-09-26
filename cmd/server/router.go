package main

import (
	"log/slog"
	"net/http"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func newRouter(h *handler.Handler, tokens *auth.Tokens, logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestContext(logger), middleware.Recovery(logger), middleware.CORS())
	r.NoRoute(func(c *gin.Context) { web.Error(c, http.StatusNotFound, "NOT_FOUND", "Recurso no encontrado", nil) })
	// A stream must not inherit the REST deadline. Authentication still runs
	// before the SSE handler, and the handler revalidates the session while open.
	stream := r.Group("/v1")
	stream.Use(middleware.RequireAuth(tokens, h.Repo))
	stream.GET("/clientes/me/tarjetas/events", h.CardEventsStream)
	rest := r.Group("")
	rest.Use(middleware.Timeout(12 * time.Second))
	v1 := rest.Group("/v1")
	registerPublicRoutes(v1, h)
	// Every domain below inherits authentication before its routes are registered.
	authenticated := v1.Group("")
	authenticated.Use(middleware.RequireAuth(tokens, h.Repo))
	authenticated.GET("/me", h.Me)
	authenticated.PATCH("/me", h.UpdateMe)
	authenticated.GET("/me/foto", h.ProfilePhoto)
	authenticated.POST("/me/foto", h.UploadProfilePhoto)
	authenticated.GET("/me/export", h.ExportMe)
	authenticated.DELETE("/me", h.DeleteMe)
	authenticated.POST("/auth/logout", h.Logout)
	registerMerchantRoutes(authenticated, h)
	registerCustomerRoutes(authenticated, h)
	registerMovementRoutes(authenticated, h)
	registerLegacyRoutes(rest, h, middleware.RequireAuth(tokens, h.Repo))
	return r
}

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
	r.Use(middleware.RequestContext(logger), middleware.Timeout(12*time.Second), middleware.Recovery(logger), middleware.CORS())
	r.NoRoute(func(c *gin.Context) { web.Error(c, http.StatusNotFound, "NOT_FOUND", "Recurso no encontrado", nil) })
	v1 := r.Group("/v1")
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
	registerLegacyRoutes(r, h, middleware.RequireAuth(tokens, h.Repo))
	return r
}

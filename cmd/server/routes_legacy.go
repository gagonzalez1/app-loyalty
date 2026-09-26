package main

import (
	"clientesFrecuentes/internal/handler"

	"github.com/gin-gonic/gin"
)

func registerLegacyRoutes(r *gin.RouterGroup, h *handler.Handler, requireAuth gin.HandlerFunc) {
	r.POST("/auth/register", h.LegacyRegister)
	r.POST("/auth/login", h.LegacyLogin)
	r.POST("/auth/google", h.LegacyGoogle)
	r.GET("/me", requireAuth, h.LegacyMe)
}

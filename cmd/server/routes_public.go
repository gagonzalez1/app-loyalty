package main

import (
	"clientesFrecuentes/internal/handler"

	"github.com/gin-gonic/gin"
)

func registerPublicRoutes(r *gin.RouterGroup, h *handler.Handler) {
	r.GET("/health/live", h.HealthLive)
	r.GET("/health/ready", h.HealthReady)
	r.GET("/version", h.Version)
	r.POST("/auth/register", h.RegisterCustomer)
	r.POST("/auth/login", h.Login)
	r.POST("/auth/google", h.LoginGoogle)
	r.POST("/auth/refresh", h.Refresh)
	r.POST("/auth/email-verification/request", h.RequestEmailVerification)
	r.POST("/auth/email-verification/confirm", h.ConfirmEmailVerification)
	r.POST("/auth/password-reset/request", h.RequestPasswordReset)
	r.POST("/auth/password-reset/confirm", h.ConfirmPasswordReset)
	r.POST("/demo/comercios", h.RegisterDemoMerchant)
	r.GET("/invitaciones/:token", h.PublicInvitation)
	r.POST("/invitaciones/:token/registrar", h.RegisterInvitation)
	r.POST("/mercado-pago/webhooks", h.MercadoPagoWebhook)
}

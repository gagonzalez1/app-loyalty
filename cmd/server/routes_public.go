package main

import (
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"

	"github.com/gin-gonic/gin"
)

func registerPublicRoutes(r *gin.RouterGroup, h *handler.Handler) {
	r.GET("/public/sucursales/:branch_id/menu", h.PublicBranchMenu)
	r.GET("/public/marcas/:brand_id/menus", h.PublicBrandMenus)
	r.GET("/health/live", h.HealthLive)
	r.GET("/health/ready", h.HealthReady)
	r.GET("/version", h.Version)
	r.GET("/email-assets/v1/:name", h.EmailBrandAsset)
	r.GET("/suscripciones/precios", h.SubscriptionPrices)
	r.POST("/auth/register", h.RegisterCustomer)
	r.POST("/auth/login", h.Login)
	r.POST("/auth/google", h.LoginGoogle)
	r.POST("/auth/apple/challenge", func(c *gin.Context) {
		if c.GetHeader("Authorization") != "" {
			middleware.RequireAuth(h.Tokens, h.Repo)(c)
		} else {
			c.Next()
		}
	}, h.AppleChallenge)
	r.POST("/auth/apple", h.LoginApple)
	r.POST("/apple/app-store/notifications", h.AppleAppStoreNotification)
	r.POST("/auth/refresh", h.Refresh)
	r.POST("/auth/logout", h.Logout)
	r.GET("/auth/challenge", h.Challenge)
	r.POST("/auth/challenge/verify", h.VerifyChallenge)
	r.POST("/auth/email-verification/request", h.RequestEmailVerification)
	r.POST("/auth/email-verification/code/request", h.RequestEmailVerificationCode)
	r.POST("/auth/email-verification/code/confirm", h.ConfirmEmailVerificationCode)
	r.POST("/auth/email-verification/confirm", h.ConfirmEmailVerification)
	r.POST("/auth/email-change/confirm", h.ConfirmEmailChange)
	r.POST("/auth/password-reset/request", h.RequestPasswordReset)
	r.POST("/auth/password-reset/confirm", h.ConfirmPasswordReset)
	r.POST("/demo/comercios", h.RegisterDemoMerchant)
	r.POST("/referidos/validacion", h.ValidateReferralCode)
	r.GET("/invitaciones/:token", h.PublicInvitation)
	r.POST("/invitaciones/:token/registrar", h.RegisterInvitation)
	r.POST("/mercado-pago/webhooks", h.MercadoPagoWebhook)
}

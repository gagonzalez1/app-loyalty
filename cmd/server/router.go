package main

import (
	"log/slog"
	"net/http"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/challenge"
	"clientesFrecuentes/internal/handler"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func newRouter(h *handler.Handler, tokens *auth.Tokens, logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestContext(logger), middleware.Recovery(logger), middleware.CORS(), func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, private")
		c.Header("Pragma", "no-cache")
		c.Request = c.Request.WithContext(challenge.WithReceipt(c.Request.Context(), c.GetHeader("X-Captcha-Token")))
		c.Next()
	})
	r.NoRoute(func(c *gin.Context) { web.Error(c, http.StatusNotFound, "NOT_FOUND", "Recurso no encontrado", nil) })
	// A stream must not inherit the REST deadline. Authentication still runs
	// before the SSE handler, and the handler revalidates the session while open.
	stream := r.Group("/v1")
	stream.Use(middleware.RequireAuth(tokens, h.Repo))
	stream.Use(h.RequireAdult)
	stream.GET("/clientes/me/tarjetas/events", h.CardEventsStream)
	rest := r.Group("")
	rest.Use(middleware.Timeout(12 * time.Second))
	v1 := rest.Group("/v1")
	registerPublicRoutes(v1, h)
	backoffice := v1.Group("/backoffice")
	backoffice.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		c.Next()
	})
	backoffice.POST("/login", h.BackofficeLogin)
	backofficeAuthorized := backoffice.Group("")
	backofficeAuthorized.Use(h.RequireBackoffice)
	backofficeAuthorized.GET("/me", h.BackofficeMe)
	backofficeAuthorized.GET("/customers", h.BackofficeCustomers)
	backofficeAuthorized.GET("/users", h.BackofficeRegisteredUsers)
	backofficeAuthorized.GET("/prices", h.SubscriptionPrices)
	backofficeAuthorized.GET("/prices/:program/preview", h.BackofficePricePreview)
	backofficeAuthorized.PUT("/prices/:program", h.BackofficeChangePrice)
	backofficeAuthorized.GET("/price-changes", h.BackofficePriceHistory)
	backofficeAuthorized.GET("/price-changes/:id", h.BackofficePriceChange)
	backofficeAuthorized.POST("/price-changes/:id/retry", h.BackofficeRetryPriceChange)
	backofficeAuthorized.POST("/logout", h.BackofficeLogout)
	backofficeAuthorized.GET("/campaigns", h.BackofficeCampaigns)
	backofficeAuthorized.POST("/campaigns", h.BackofficeCreateCampaign)
	backofficeAuthorized.PUT("/campaigns/:id", h.BackofficeUpdateCampaign)
	backofficeAuthorized.PATCH("/campaigns/:id/active", h.BackofficeCampaignActive)
	backofficeAuthorized.GET("/influencers", h.BackofficeInfluencers)
	backofficeAuthorized.GET("/influencers/:id/earnings", h.BackofficeInfluencerEarnings)
	backofficeAuthorized.POST("/influencers", h.BackofficeCreateInfluencer)
	backofficeAuthorized.GET("/codes", h.BackofficeCodes)
	backofficeAuthorized.POST("/codes", h.BackofficeCreateCode)
	backofficeAuthorized.PATCH("/codes/:id/active", h.BackofficeCodeActive)
	backofficeAuthorized.GET("/attributions", h.BackofficeAttributions)
	backofficeAuthorized.GET("/rewards", h.BackofficeRewards)
	backofficeAuthorized.GET("/metrics", h.BackofficeMetrics)
	backofficeAuthorized.POST("/rewards/:invoice/settle", h.BackofficeSettleReward)
	backofficeAuthorized.GET("/brands/:id/credit-allocations", h.BackofficeMerchantCreditAllocations)
	backofficeAuthorized.POST("/brands/:id/credit-allocations", h.BackofficeRecordMerchantCredit)
	// Every domain below inherits authentication before its routes are registered.
	authenticated := v1.Group("")
	authenticated.Use(middleware.RequireAuth(tokens, h.Repo))
	authenticated.GET("/me", h.Me)
	authenticated.POST("/me/age-confirmation", h.ConfirmAdult)
	authenticated.PATCH("/me", h.UpdateMe)
	authenticated.POST("/me/email-change/request", h.RequestEmailChange)
	authenticated.GET("/me/foto", h.ProfilePhoto)
	authenticated.POST("/me/foto", h.UploadProfilePhoto)
	authenticated.GET("/me/export", h.ExportMe)
	authenticated.DELETE("/me", h.DeleteMe)
	authenticated.POST("/auth/google/link", h.LinkGoogle)
	authenticated.POST("/auth/reauthenticate", h.Reauthenticate)
	authenticated.Use(h.RequireAdult)
	registerMerchantRoutes(authenticated, h)
	registerCustomerRoutes(authenticated, h)
	registerMovementRoutes(authenticated, h)
	registerLegacyRoutes(rest, h, middleware.RequireAuth(tokens, h.Repo), h.RequireAdult)
	return r
}

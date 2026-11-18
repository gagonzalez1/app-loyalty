package main

import (
	"clientesFrecuentes/internal/handler"

	"github.com/gin-gonic/gin"
)

func registerCustomerRoutes(r *gin.RouterGroup, h *handler.Handler) {
	r.POST("/clientes/me/gift-cards/canjear", h.RedeemGiftCard)
	r.GET("/clientes/me", h.Customer)
	r.GET("/clientes/me/tiendas", h.CustomerStoreLocations)
	r.GET("/clientes/me/tiendas/:branch_id/resena-google", h.CustomerStoreGoogleReview)
	r.PUT("/clientes/me/tiendas/:branch_id/favorito", h.SetCustomerStoreFavorite)
	r.DELETE("/clientes/me/tiendas/:branch_id/favorito", h.DeleteCustomerStoreFavorite)
	r.GET("/clientes/me/resenas/pendientes", h.PendingReviews)
	r.POST("/clientes/me/resenas/:invitation_id/reserva", h.ReserveReview)
	r.POST("/clientes/me/resenas/:invitation_id/eventos", h.ReviewEvent)
	r.GET("/clientes/me/tarjetas", h.Cards)
	r.POST("/clientes/me/push-token", h.SavePushToken)
	r.GET("/clientes/me/web-push/key", h.WebPushKey)
	r.PUT("/clientes/me/web-push/subscription", h.SaveWebPushSubscription)
	r.DELETE("/clientes/me/web-push/subscription", h.DeleteWebPushSubscription)
	r.GET("/clientes/me/movimientos", h.CustomerMovements)
	r.GET("/clientes/me/tarjetas/:card_id/movimientos", h.CardMovements)
}

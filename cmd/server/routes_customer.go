package main

import (
	"clientesFrecuentes/internal/handler"

	"github.com/gin-gonic/gin"
)

func registerCustomerRoutes(r *gin.RouterGroup, h *handler.Handler) {
	r.GET("/clientes/me", h.Customer)
	r.GET("/clientes/me/tarjetas", h.Cards)
	r.POST("/clientes/me/push-token", h.SavePushToken)
	r.GET("/clientes/me/movimientos", h.CustomerMovements)
	r.GET("/clientes/me/tarjetas/:card_id/movimientos", h.CardMovements)
}

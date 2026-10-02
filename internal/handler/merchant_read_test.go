package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/service"

	"github.com/gin-gonic/gin"
)

func TestMerchantReadEndpointsRejectCustomerAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{Service: &service.Service{}}
	tests := []struct {
		name string
		path string
		call gin.HandlerFunc
	}{
		{name: "customers", path: "/marcas/1/clientes", call: h.BrandCustomers},
		{name: "metrics", path: "/marcas/1/metricas/resumen", call: h.BrandMetrics},
		{name: "period", path: "/marcas/1/metricas/periodo", call: h.BrandPeriodMetrics},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.GET(tt.path, func(c *gin.Context) {
				c.Set(middleware.ActorKey, middleware.Actor{ID: 1, AccountType: "CLIENTE_FINAL"})
				tt.call(c)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"FORBIDDEN"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBrandCustomersRejectsSearchOver120Runes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{Service: &service.Service{}}
	router := gin.New()
	router.GET("/marcas/:brand_id/clientes", func(c *gin.Context) {
		c.Set(middleware.ActorKey, middleware.Actor{ID: 1, AccountType: "PERSONAL_MARCA"})
		h.BrandCustomers(c)
	})
	path := "/marcas/1/clientes?search=" + url.QueryEscape(strings.Repeat("á", 121))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPeriodMetricsRejectsInvalidQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{Service: &service.Service{}}
	router := gin.New()
	router.GET("/marcas/:brand_id/metricas/periodo", func(c *gin.Context) {
		c.Set(middleware.ActorKey, middleware.Actor{ID: 1, AccountType: "PERSONAL_MARCA"})
		h.BrandPeriodMetrics(c)
	})
	for _, path := range []string{"/marcas/1/metricas/periodo", "/marcas/1/metricas/periodo?period=year", "/marcas/1/metricas/periodo?period=day&date=2026-02-30", "/marcas/0/metricas/periodo?period=day"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

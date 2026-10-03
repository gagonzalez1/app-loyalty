package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCORSPreflightAllowsVersionedMutations(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.puntazo.test")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS())
	router.PATCH("/resource", func(c *gin.Context) {
		c.Header("ETag", `"7"`)
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/resource", nil)
	request.Header.Set("Origin", "https://app.puntazo.test")
	request.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	request.Header.Set("Access-Control-Request-Headers", "authorization,content-type,if-match,idempotency-key,x-client-platform")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.puntazo.test" {
		t.Fatalf("allow origin=%q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials=%q", got)
	}
	allowMethods := response.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowMethods, http.MethodPatch) || !strings.Contains(allowMethods, http.MethodDelete) {
		t.Fatalf("allow methods=%q", allowMethods)
	}
	allowHeaders := strings.ToLower(response.Header().Get("Access-Control-Allow-Headers"))
	for _, required := range []string{"authorization", "content-type", "if-match", "idempotency-key", "x-client-platform"} {
		if !strings.Contains(allowHeaders, required) {
			t.Fatalf("allow headers=%q missing=%q", allowHeaders, required)
		}
	}
}

func TestCORSRequiresExplicitHTTPSAllowlistInProduction(t *testing.T) {
	for _, origins := range []string{"", "*", "http://app.puntazo.test"} {
		t.Run(origins, func(t *testing.T) {
			t.Setenv("APP_ENV", "production")
			t.Setenv("CORS_ORIGINS", origins)
			defer func() {
				if recover() == nil {
					t.Fatal("production CORS misconfiguration did not fail fast")
				}
			}()
			_ = CORS()
		})
	}
}

func TestCORSExposesETag(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.puntazo.test")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS())
	router.GET("/resource", func(c *gin.Context) {
		c.Header("ETag", `"7"`)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/resource", nil)
	request.Header.Set("Origin", "https://app.puntazo.test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if exposed := strings.ToLower(response.Header().Get("Access-Control-Expose-Headers")); !strings.Contains(exposed, "etag") {
		t.Fatalf("exposed headers=%q", exposed)
	}
}

func TestCORSAllowsCardEventStreamHeaders(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://testing.puntazo.pro")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS())
	router.GET("/v1/clientes/me/tarjetas/events", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, origin := range []string{"https://testing.puntazo.pro", "https://untrusted.example"} {
		request := httptest.NewRequest(http.MethodOptions, "/v1/clientes/me/tarjetas/events", nil)
		request.Header.Set("Origin", origin)
		request.Header.Set("Access-Control-Request-Method", "GET")
		request.Header.Set("Access-Control-Request-Headers", "authorization,cache-control,x-client-platform,last-event-id")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if origin != "https://testing.puntazo.pro" {
			if response.Code != http.StatusForbidden {
				t.Fatalf("untrusted origin status=%d", response.Code)
			}
			continue
		}
		if response.Code != http.StatusNoContent {
			t.Fatalf("preflight status=%d", response.Code)
		}
		allowed := "," + strings.ToLower(response.Header().Get("Access-Control-Allow-Headers")) + ","
		for _, header := range []string{"authorization", "cache-control", "x-client-platform", "last-event-id"} {
			if !strings.Contains(allowed, ","+header+",") {
				t.Fatalf("browser would reject stream: missing %s in %s", header, allowed)
			}
		}
	}
}

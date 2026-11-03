package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/service"
	"github.com/gin-gonic/gin"
)

func TestAppleAuthHTTPContractFailsClosedWithoutProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{Service: &service.Service{}, Limiter: middleware.NewRateLimiter()}
	router := gin.New()
	router.POST("/challenge", h.AppleChallenge)
	router.POST("/login", h.LoginApple)
	router.POST("/link", h.LinkApple)
	cases := []struct {
		path, body, code string
		status           int
	}{
		{"/challenge", `{"purpose":"login"}`, "APPLE_AUTH_UNAVAILABLE", 503},
		{"/challenge", `{"purpose":"unknown"}`, "INVALID_REQUEST", 422},
		{"/challenge", `{"purpose":"link"}`, "UNAUTHENTICATED", 401},
		{"/challenge", `{"purpose":"reauthenticate"}`, "UNAUTHENTICATED", 401},
		{"/login", `{"challenge_id":"id","id_token":"secret-token","authorization_code":"secret-code","name":"A","apellido":"B","account_type":"CLIENTE_FINAL"}`, "APPLE_AUTH_UNAVAILABLE", 503},
		{"/login", `{"challenge_id":"id","id_token":"secret-token","authorization_code":"secret-code","tipo_cuenta":"CLIENTE_FINAL"}`, "APPLE_AUTH_UNAVAILABLE", 503},
		{"/login", `{"challenge_id":"id","unexpected":true}`, "INVALID_REQUEST", 422},
		{"/link", `{"version":1}`, "UNAUTHENTICATED", 401},
	}
	for _, test := range cases {
		t.Run(test.path+"/"+test.code, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Client-Platform", "native")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret-token") || strings.Contains(response.Body.String(), "secret-code") {
				t.Fatal("credentials leaked into error envelope")
			}
		})
	}
}

func TestAppleReauthenticationHTTPRejectsMixedProofs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{Service: &service.Service{}, Limiter: middleware.NewRateLimiter()}
	router := gin.New()
	router.POST("/reauth", func(c *gin.Context) {
		c.Set(middleware.ActorKey, middleware.Actor{ID: 1, SessionID: "test-session", AuthVersion: 1})
		c.Next()
	}, h.Reauthenticate)
	request := httptest.NewRequest(http.MethodPost, "/reauth", strings.NewReader(`{"version":1,"password":"secret-password","apple":{"challenge_id":"test-id","id_token":"secret-token","authorization_code":"secret-code"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Client-Platform", "native")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 422 || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("status=%d response=%s", response.Code, response.Body.String())
	}
}

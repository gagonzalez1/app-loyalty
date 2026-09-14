package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"

	"github.com/gin-gonic/gin"
)

func TestAccountTypeRequiredUsesStableErrorOnGoogleRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"/v1/auth/google", "/auth/google"} {
		t.Run(route, func(t *testing.T) {
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPost, route, nil)
			writeErr(c, service.ErrAccountTypeRequired)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						NextAction string `json:"next_action"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "ACCOUNT_TYPE_REQUIRED" || body.Error.Details.NextAction != "SELECT_ACCOUNT_TYPE" {
				t.Fatalf("error=%+v body=%s", body.Error, response.Body.String())
			}
		})
	}
}

func TestDecodeGoogleAuthAcceptsAccountSelectionAndRejectsUnknownNestedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"id_token":"verified-token","account_type":"PERSONAL_MARCA","merchant_registration":{"brand_name":"Marca","branch_name":"Principal","branch_address":"Calle 1","program_type":"SELLOS","access_code":"demo-access-code"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/google", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	var got model.GoogleAuthRequest
	if err := decode(c, &got); err != nil {
		t.Fatal(err)
	}
	if got.AccountType == nil || *got.AccountType != "PERSONAL_MARCA" || got.MerchantRegistration == nil || got.MerchantRegistration.ProgramType != "SELLOS" {
		t.Fatalf("decoded request=%+v", got)
	}

	unknown := `{"id_token":"verified-token","account_type":"PERSONAL_MARCA","merchant_registration":{"brand_name":"Marca","branch_name":"Principal","program_type":"SELLOS","access_code":"demo-access-code","owner_name":"must-not-be-client-controlled"}}`
	request = httptest.NewRequest(http.MethodPost, "/v1/auth/google", strings.NewReader(unknown))
	request.Header.Set("Content-Type", "application/json")
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	if err := decode(c, &model.GoogleAuthRequest{}); err == nil {
		t.Fatal("unknown merchant_registration field was accepted")
	}
}

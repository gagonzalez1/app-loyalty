package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"clientesFrecuentes/internal/model"

	"github.com/gin-gonic/gin"
)

func TestDecodeAccountPatchPreservesOmittedNullAndValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest("PATCH", "/v1/me", strings.NewReader(`{"apellido":null,"alias":" apodo "}`))
	req.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req

	var patch model.UpdateAccountRequest
	if err := decode(ctx, &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Name.Set || !patch.LastName.Set || patch.LastName.Value != nil || !patch.Alias.Set || patch.Alias.Value == nil || *patch.Alias.Value != " apodo " {
		t.Fatalf("decoded patch=%+v", patch)
	}
}

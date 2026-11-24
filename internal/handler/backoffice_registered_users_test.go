package handler

import (
	"net/http/httptest"
	"testing"

	"clientesFrecuentes/internal/repository"
	"github.com/gin-gonic/gin"
)

func TestRegisteredUsersRejectsFinanceAndInvalidPagination(t *testing.T) {
	for _, test := range []struct {
		role, query string
		status      int
	}{
		{"FINANZAS", "", 403},
		{"ADMIN_SISTEMA", "?limit=0", 422},
		{"ADMIN_SISTEMA", "?limit=101", 422},
		{"ADMIN_SISTEMA", "?limit=invalid", 422},
		{"ADMIN_SISTEMA", "?before=-1", 422},
		{"ADMIN_SISTEMA", "?before=0", 422},
		{"ADMIN_SISTEMA", "?before=invalid", 422},
	} {
		t.Run(test.role+test.query, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest("GET", "/v1/backoffice/users"+test.query, nil)
			ctx.Set("backoffice_user", repository.BackofficeUser{Role: test.role})
			(&Handler{}).BackofficeRegisteredUsers(ctx)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

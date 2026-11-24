package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"clientesFrecuentes/internal/service"
	"github.com/gin-gonic/gin"
)

func TestMenuPDFErrorsUseStableCodes(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrMenuPDFTooLarge, 413, "MENU_PDF_TOO_LARGE"},
		{service.ErrMenuPDFType, 415, "MENU_PDF_TYPE_UNSUPPORTED"},
		{service.ErrMenuPDFInvalid, 422, "MENU_PDF_INVALID"},
		{service.ErrMenuPDFPassword, 422, "MENU_PDF_PASSWORD_PROTECTED"},
		{service.ErrMenuStorage, 503, "MENU_STORAGE_UNAVAILABLE"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			r := gin.New()
			h := &Handler{}
			r.GET("/", func(c *gin.Context) { h.writeMenuError(c, tt.err) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.code) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tt.err == service.ErrMenuPDFTooLarge && !strings.Contains(w.Body.String(), "30 MiB") {
				t.Fatalf("outdated size guidance: %s", w.Body.String())
			}
		})
	}
}

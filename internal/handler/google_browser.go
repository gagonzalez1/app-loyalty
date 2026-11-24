package handler

import (
	"mime"
	"net/http"
	"time"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GoogleBrowserConfig(c *gin.Context) {
	c.JSON(http.StatusOK, web.Envelope[model.GoogleBrowserConfig]{
		Data: model.GoogleBrowserConfig{Enabled: h.Service.GoogleBrowserEnabled()}, RequestID: web.RequestID(c),
	})
}

func (h *Handler) StartGoogleBrowser(c *gin.Context) {
	var req model.GoogleBrowserStartRequest
	if !decodeGoogleBrowserJSON(c, &req) || !h.limit(c, "google-browser-start:ip:"+h.clientIP(c), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.StartGoogleBrowser(c.Request.Context(), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.GoogleBrowserStart]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) GoogleBrowserCallback(c *gin.Context) {
	c.Header("Referrer-Policy", "no-referrer")
	if !h.allowLimit(c, "google-browser-callback:ip:"+h.clientIP(c), 120, time.Minute) {
		return
	}
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/x-www-form-urlencoded" {
		writeErr(c, service.ErrGoogleBrowserInvalid)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if err = c.Request.ParseForm(); err != nil {
		writeErr(c, service.ErrGoogleBrowserInvalid)
		return
	}
	// Use only the submitted form, never the request's query parameters.
	form := c.Request.PostForm
	for _, key := range []string{"state", "id_token", "error", "iss"} {
		if len(form[key]) > 1 {
			writeErr(c, service.ErrGoogleBrowserInvalid)
			return
		}
	}
	if issuer := form.Get("iss"); issuer != "" && issuer != "https://accounts.google.com" {
		writeErr(c, service.ErrGoogleBrowserInvalid)
		return
	}
	target, err := h.Service.ReceiveGoogleBrowserCallback(c.Request.Context(), form.Get("state"), form.Get("id_token"), form.Get("error"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("Location", target)
	c.Status(http.StatusSeeOther)
}

func (h *Handler) FinishGoogleBrowser(c *gin.Context) {
	var req model.GoogleBrowserFinishRequest
	if !decodeGoogleBrowserJSON(c, &req) || !h.allowLimit(c, "google-browser-finish:ip:"+h.clientIP(c), 120, time.Minute) {
		return
	}
	data, err := h.Service.FinishGoogleBrowser(c.Request.Context(), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.GoogleBrowserFinish]{Data: data, RequestID: web.RequestID(c)})
}

func decodeGoogleBrowserJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	if c.GetHeader(clientPlatformHeader) != "web" || decode(c, dst) != nil {
		writeErr(c, service.ErrGoogleBrowserInvalid)
		return false
	}
	return true
}

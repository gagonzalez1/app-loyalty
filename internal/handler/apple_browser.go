package handler

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) StartAppleBrowser(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.AppleBrowserStartRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "apple-browser-start:ip:"+h.clientIP(c), loginAttempts, loginWindow) {
		return
	}
	a, authenticated := middleware.CurrentActor(c)
	if req.Purpose == "reauthenticate" && !authenticated {
		actor(c)
		return
	}
	if authenticated && !h.limit(c, "apple-browser-start:user:"+strconv.FormatInt(a.ID, 10), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.StartAppleBrowser(c.Request.Context(), req, platform, a.ID, a.SessionID, a.AuthVersion)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.AppleBrowserStart]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) AppleBrowserCallback(c *gin.Context) {
	c.Header("Referrer-Policy", "no-referrer")
	if !h.allowLimit(c, "apple-browser-callback:ip:"+h.clientIP(c), 240, time.Minute) {
		return
	}
	// Apple's authorization page makes a cross-site form navigation. Only
	// fields in that form are used, and each recognized field must be unique.
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if err = c.Request.ParseForm(); err != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	for _, key := range []string{"state", "code", "id_token", "error"} {
		if len(c.Request.PostForm[key]) > 1 {
			writeErr(c, service.ErrInvalidRequest)
			return
		}
	}
	target, err := h.Service.ReceiveAppleBrowserCallback(c.Request.Context(), c.Request.PostForm.Get("state"), c.Request.PostForm.Get("code"),
		c.Request.PostForm.Get("id_token"), c.Request.PostForm.Get("error"))
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("Location", target)
	c.Status(http.StatusSeeOther)
}

func (h *Handler) FinishAppleBrowser(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.AppleBrowserFinishRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	// Pending PWA/browser returns may poll without starting new attempts.
	if !h.allowLimit(c, "apple-browser-finish:ip:"+h.clientIP(c), 180, time.Minute) {
		return
	}
	a, authenticated := middleware.CurrentActor(c)
	if authenticated {
		version, ok := accountVersion(c)
		if !ok {
			return
		}
		if version != req.Version {
			writeErr(c, repository.ErrPreconditionFailed)
			return
		}
	} else if req.Version != 0 {
		actor(c)
		return
	}
	data, err := h.Service.FinishAppleBrowser(c.Request.Context(), req, platform, a.ID, a.SessionID, a.AuthVersion)
	if errors.Is(err, repository.ErrAppleLinkRequired) {
		web.Error(c, http.StatusConflict, "APPLE_LINK_REQUIRED", "Vinculá Apple a tu cuenta desde la app de iPhone y volvé a intentar", nil)
		return
	}
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAuth(c, http.StatusOK, platform, data)
}

package handler

import (
	"net/http"
	"strings"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func (h *Handler) RegisterCustomer(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.RegisterCustomerRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "customer-register:ip:"+h.clientIP(c), customerSignupAttempts, customerSignupWindow) {
		return
	}
	data, err := h.Service.RegisterCustomer(c.Request.Context(), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeCustomerRegistration(c, http.StatusCreated, platform, data)
}

func (h *Handler) Login(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.LoginRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "login:ip:"+h.clientIP(c), loginAttempts, loginWindow) || !h.limit(c, "login:email:"+strings.ToLower(strings.TrimSpace(req.Email)), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.Login(c.Request.Context(), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAuth(c, http.StatusOK, platform, data)
}

func (h *Handler) Refresh(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var refreshToken string
	if platform == "web" {
		var err error
		refreshToken, err = c.Cookie(refreshCookieName)
		if err != nil {
			writeErr(c, service.ErrInvalidCredentials)
			return
		}
	} else {
		var req model.RefreshRequest
		if decode(c, &req) != nil {
			writeErr(c, service.ErrInvalidRequest)
			return
		}
		refreshToken = req.RefreshToken
	}
	if !h.limit(c, "refresh:ip:"+h.clientIP(c), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.Refresh(c.Request.Context(), refreshToken)
	if err != nil {
		if platform == "web" {
			clearRefreshCookie(c)
		}
		writeErr(c, err)
		return
	}
	writeAuth(c, http.StatusOK, platform, data)
}

func (h *Handler) Logout(c *gin.Context) {
	platform, validPlatform := clientPlatform(c)
	if !validPlatform {
		return
	}
	a, ok := actor(c)
	if !ok {
		return
	}
	if err := h.Service.Logout(c.Request.Context(), a.ID, a.SessionID); err != nil {
		writeErr(c, err)
		return
	}
	if platform == "web" {
		clearRefreshCookie(c)
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) LoginGoogle(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.GoogleAuthRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "login-google:ip:"+h.clientIP(c), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.LoginGoogle(c.Request.Context(), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAuth(c, http.StatusOK, platform, data)
}

func (h *Handler) Me(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	data, err := h.Service.CurrentUser(c.Request.Context(), a.ID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("ETag", accountETag(data.User.Version))
	c.JSON(http.StatusOK, web.Envelope[model.CurrentUser]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) UpdateMe(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	version, ok := accountVersion(c)
	if !ok {
		return
	}
	var req model.UpdateAccountRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.UpdateCurrentUser(c.Request.Context(), a.ID, version, req)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("ETag", accountETag(data.User.Version))
	c.JSON(http.StatusOK, web.Envelope[model.CurrentUser]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) ExportMe(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	data, err := h.Service.ExportCurrentUser(c.Request.Context(), a.ID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.AccountExport]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) DeleteMe(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	version, ok := accountVersion(c)
	if !ok {
		return
	}
	var req model.AnonymizeAccountRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	data, err := h.Service.AnonymizeCurrentUser(c.Request.Context(), a.ID, version, req)
	if err != nil {
		writeErr(c, err)
		return
	}
	clearRefreshCookie(c)
	c.JSON(http.StatusAccepted, web.Envelope[model.Anonymization]{Data: data, RequestID: web.RequestID(c)})
}

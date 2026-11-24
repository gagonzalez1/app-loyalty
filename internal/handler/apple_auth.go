package handler

import (
	"net/http"
	"strconv"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"
	"github.com/gin-gonic/gin"
)

func (h *Handler) AppleChallenge(c *gin.Context) {
	if _, ok := clientPlatform(c); !ok {
		return
	}
	var req model.AppleChallengeRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if req.Purpose != "login" && req.Purpose != "link" && req.Purpose != "reauthenticate" {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "apple-challenge:ip:"+h.clientIP(c), loginAttempts, loginWindow) {
		return
	}
	var userID int64
	var sessionID string
	if req.Purpose != "login" {
		a, ok := actor(c)
		if !ok {
			return
		}
		userID, sessionID = a.ID, a.SessionID
		if !h.limit(c, "apple-challenge:user:"+strconv.FormatInt(userID, 10), loginAttempts, loginWindow) {
			return
		}
	}
	data, err := h.Service.CreateAppleChallenge(c.Request.Context(), req.Purpose, userID, sessionID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[model.AppleChallenge]{Data: data, RequestID: web.RequestID(c)})
}

func (h *Handler) LoginApple(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	var req model.AppleAuthRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "login-apple:ip:"+h.clientIP(c), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.LoginApple(c.Request.Context(), req)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAuth(c, http.StatusOK, platform, data)
}

func (h *Handler) LinkApple(c *gin.Context) {
	platform, ok := clientPlatform(c)
	if !ok {
		return
	}
	a, ok := actor(c)
	if !ok {
		return
	}
	var req model.AppleLinkRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "apple-link:ip:"+h.clientIP(c), loginAttempts, loginWindow) || !h.limit(c, "apple-link:user:"+strconv.FormatInt(a.ID, 10), loginAttempts, loginWindow) {
		return
	}
	data, err := h.Service.LinkApple(c.Request.Context(), a.ID, a.SessionID, a.AuthVersion, a.AuthTime, req)
	if err != nil {
		writeErr(c, err)
		return
	}
	writeAuth(c, http.StatusOK, platform, data)
}

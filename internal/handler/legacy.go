package handler

import (
	"net/http"
	"strings"

	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"

	"github.com/gin-gonic/gin"
)

func (h *Handler) LegacyRegister(c *gin.Context) {
	var req model.LegacyRegisterRequest
	if decode(c, &req) != nil {
		writeErr(c, service.ErrInvalidRequest)
		return
	}
	if !h.limit(c, "customer-register:ip:"+h.clientIP(c), customerSignupAttempts, customerSignupWindow) {
		return
	}
	data, err := h.Service.RegisterCustomer(c.Request.Context(), model.RegisterCustomerRequest{Email: req.Email, Password: req.Password, Name: req.Name})
	if err != nil {
		writeErr(c, err)
		return
	}
	if data.Session == nil {
		c.JSON(http.StatusCreated, gin.H{"verification_required": true, "usuario": data.User})
		return
	}
	c.JSON(http.StatusCreated, legacyAuth(model.AuthData{Session: *data.Session, User: data.User}))
}

func (h *Handler) LegacyLogin(c *gin.Context) {
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
	c.JSON(http.StatusOK, legacyAuth(data))
}

func (h *Handler) LegacyGoogle(c *gin.Context) {
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
	c.JSON(http.StatusOK, legacyAuth(data))
}

func (h *Handler) LegacyMe(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	data, err := h.Service.CurrentUser(c.Request.Context(), a.ID)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, model.LegacyUser{ID: data.User.ID, Email: data.User.Email, Name: data.User.Name, Role: legacyRole(data.User.AccountType)})
}

func legacyAuth(data model.AuthData) model.LegacyAuth {
	return model.LegacyAuth{Token: data.Session.AccessToken, User: model.LegacyUser{ID: data.User.ID, Email: data.User.Email, Name: data.User.Name, Role: legacyRole(data.User.AccountType)}}
}

func legacyRole(accountType string) string {
	if accountType == "PERSONAL_MARCA" {
		return "TIENDA"
	}
	return "CLIENTE_FINAL"
}

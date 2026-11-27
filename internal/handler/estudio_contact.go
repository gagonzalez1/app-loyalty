package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"clientesFrecuentes/internal/auth"
	"clientesFrecuentes/internal/middleware"
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type estudioGrant struct {
	Token     string               `json:"access_token"`
	ExpiresAt time.Time            `json:"expires_at"`
	Contact   model.EstudioContact `json:"contact"`
}

func (h *Handler) CreateEstudioGrant(c *gin.Context) {
	a, ok := actor(c)
	if !ok {
		return
	}
	if a.AccountType != "PERSONAL_MARCA" {
		writeErr(c, service.ErrForbidden)
		return
	}
	brand, branch, err := ids(c)
	if err != nil {
		writeErr(c, err)
		return
	}
	contact, err := h.Repo.EstudioContact(c.Request.Context(), a.ID, brand, branch)
	if err != nil {
		writeErr(c, err)
		return
	}
	token, expires, err := h.Tokens.GenerateEstudio(a.ID, brand, branch, a.SessionID, a.AuthVersion, a.AuthTime)
	if err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, web.Envelope[estudioGrant]{Data: estudioGrant{Token: token, ExpiresAt: expires, Contact: contact}, RequestID: web.RequestID(c)})
}

const estudioScopeKey = "estudio_contact_scope"

func (h *Handler) RequireEstudioGrant(c *gin.Context) {
	raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok || len(raw) > 4096 {
		web.AbortError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sesión de Estudio inválida o expirada", nil)
		return
	}
	id, scope, err := h.Tokens.ParseEstudio(raw)
	if err != nil {
		web.AbortError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sesión de Estudio inválida o expirada", nil)
		return
	}
	account, err := h.Repo.ActiveSessionAccountType(c.Request.Context(), id, scope.ID, scope.AuthVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		web.AbortError(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "No se pudo verificar la sesión", nil)
		return
	}
	if account != "PERSONAL_MARCA" {
		web.AbortError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sesión de Estudio inválida o expirada", nil)
		return
	}
	c.Set(estudioScopeKey, scope)
	c.Set(middleware.ActorKey, middleware.Actor{ID: id, AccountType: account, SessionID: scope.ID, AuthVersion: scope.AuthVersion})
	c.Next()
}

func (h *Handler) EstudioContact(c *gin.Context) {
	a, _ := middleware.CurrentActor(c)
	scope := c.MustGet(estudioScopeKey).(auth.EstudioClaims)
	var contact model.EstudioContact
	var err error
	if c.Request.Method == http.MethodGet {
		contact, err = h.Repo.EstudioContact(c.Request.Context(), a.ID, scope.BrandID, scope.BranchID)
	} else {
		version, ok := accountVersion(c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		var patch model.EstudioContactPatch
		if decode(c, &patch) != nil {
			writeErr(c, service.ErrInvalidRequest)
			return
		}
		if err = h.Repo.CheckAppleBillingWrite(c.Request.Context(), a.ID, scope.BrandID); err == nil {
			contact, err = h.Service.UpdateEstudioContact(c.Request.Context(), a.ID, scope.BrandID, scope.BranchID, version, patch)
		}
	}
	if err != nil {
		writeErr(c, err)
		return
	}
	c.Header("ETag", accountETag(contact.Version))
	c.JSON(http.StatusOK, web.Envelope[model.EstudioContact]{Data: contact, RequestID: web.RequestID(c)})
}

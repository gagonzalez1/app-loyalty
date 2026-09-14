package handler

import (
	"errors"
	"net/http"

	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrMediaTooLarge):
		web.Error(c, http.StatusRequestEntityTooLarge, "MEDIA_TOO_LARGE", "La imagen supera el máximo permitido", nil)
	case errors.Is(err, service.ErrMediaType):
		web.Error(c, http.StatusUnsupportedMediaType, "MEDIA_TYPE_UNSUPPORTED", "Formato de imagen no permitido", nil)
	case errors.Is(err, service.ErrMediaUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "MEDIA_STORAGE_UNAVAILABLE", "El almacenamiento de imágenes no está disponible", nil)
	case errors.Is(err, service.ErrInvalidRequest), errors.Is(err, repository.ErrInvalidRequest):
		web.Error(c, http.StatusUnprocessableEntity, "INVALID_REQUEST", "Solicitud inválida", nil)
	case errors.Is(err, service.ErrAccountTypeRequired):
		web.Error(c, http.StatusUnprocessableEntity, "ACCOUNT_TYPE_REQUIRED", "Elegí si la cuenta es cliente o comercio", nil)
	case errors.Is(err, service.ErrInvalidCredentials):
		web.Error(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Credenciales inválidas", nil)
	case errors.Is(err, service.ErrRecentAuthRequired):
		web.Error(c, http.StatusUnauthorized, "RECENT_AUTH_REQUIRED", "Volvé a autenticarte para continuar", nil)
	case errors.Is(err, service.ErrForbidden), errors.Is(err, repository.ErrForbidden):
		web.Error(c, http.StatusForbidden, "FORBIDDEN", "Acceso denegado", nil)
	case errors.Is(err, service.ErrDemoDisabled):
		web.Error(c, http.StatusForbidden, "DEMO_SIGNUP_DISABLED", "Las altas demo están cerradas", nil)
	case errors.Is(err, service.ErrDemoAccess):
		web.Error(c, http.StatusForbidden, "DEMO_ACCESS_DENIED", "Código de acceso inválido", nil)
	case errors.Is(err, repository.ErrEmailExists):
		web.Error(c, http.StatusConflict, "EMAIL_EXISTS", "El email ya está registrado", nil)
	case errors.Is(err, repository.ErrIdempotencyConflict):
		web.Error(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "La clave ya fue usada con otra solicitud", nil)
	case errors.Is(err, repository.ErrIdempotencyInProgress):
		web.Error(c, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS", "La solicitud todavía está en curso", nil)
	case errors.Is(err, repository.ErrPreviewExpired):
		web.Error(c, http.StatusConflict, "PREVIEW_EXPIRED", "La preview venció", nil)
	case errors.Is(err, repository.ErrPreviewConsumed):
		web.Error(c, http.StatusConflict, "PREVIEW_CONSUMED", "La preview ya fue consumida", nil)
	case errors.Is(err, repository.ErrPreviewChanged):
		web.Error(c, http.StatusConflict, "PREVIEW_CHANGED", "La preview ya no coincide con el estado actual", nil)
	case errors.Is(err, repository.ErrInsufficientBalance):
		web.Error(c, http.StatusConflict, "INSUFFICIENT_BALANCE", "Saldo insuficiente", nil)
	case errors.Is(err, repository.ErrPreconditionFailed):
		web.Error(c, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "La versión del recurso cambió", nil)
	case errors.Is(err, repository.ErrOwnershipTransfer):
		web.Error(c, http.StatusConflict, "OWNERSHIP_TRANSFER_REQUIRED", "Transferí la propiedad antes de eliminar la cuenta", nil)
	case errors.Is(err, repository.ErrProgramTypeImmutable):
		web.Error(c, http.StatusConflict, "PROGRAM_TYPE_IMMUTABLE", "El tipo de programa no puede cambiar después del primer movimiento", nil)
	case errors.Is(err, repository.ErrProgramTypeHasBenefits):
		web.Error(c, http.StatusConflict, "PROGRAM_TYPE_HAS_BENEFITS", "Eliminá los beneficios antes de cambiar el tipo de programa", nil)
	case errors.Is(err, repository.ErrSelfRoleChangeForbidden):
		web.Error(c, http.StatusConflict, "SELF_ROLE_CHANGE_FORBIDDEN", "Otro administrador o propietario debe cambiar tu acceso", nil)
	case errors.Is(err, repository.ErrAccountModeConflict):
		web.Error(c, http.StatusConflict, "ACCOUNT_MODE_CONFLICT", "La cuenta tiene actividad como cliente y no puede convertirse en personal", nil)
	case errors.Is(err, repository.ErrConflict):
		web.Error(c, http.StatusConflict, "RESOURCE_CONFLICT", "La operación viola una invariante activa", nil)
	case errors.Is(err, service.ErrIdentityToken):
		web.Error(c, http.StatusUnprocessableEntity, "IDENTITY_TOKEN_INVALID", "Token inválido, vencido o utilizado", nil)
	case errors.Is(err, repository.ErrInvitationEmailMismatch):
		web.Error(c, http.StatusConflict, "INVITATION_EMAIL_MISMATCH", "La invitación pertenece a otro correo", nil)
	case errors.Is(err, repository.ErrInvitationInvalid):
		web.Error(c, http.StatusUnprocessableEntity, "INVITATION_INVALID", "Invitación inválida, vencida o utilizada", nil)
	case errors.Is(err, service.ErrEmailUnverified):
		web.Error(c, http.StatusForbidden, "EMAIL_VERIFICATION_REQUIRED", "Verificá tu correo antes de iniciar sesión", nil)
	case errors.Is(err, repository.ErrNotFound):
		web.Error(c, http.StatusNotFound, "NOT_FOUND", "Recurso no encontrado", nil)
	default:
		web.Error(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error interno", nil)
	}
}

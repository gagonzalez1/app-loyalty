package handler

import (
	"errors"
	"net/http"

	"clientesFrecuentes/internal/identitycode"
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"clientesFrecuentes/internal/web"

	"github.com/gin-gonic/gin"
)

func writeErr(c *gin.Context, err error) {
	var profile *service.SignupProfileError
	if errors.As(err, &profile) {
		missing := []string{}
		if identitycode.Initial(profile.Name) == "" {
			missing = append(missing, "name")
		}
		if identitycode.Initial(profile.LastName) == "" {
			missing = append(missing, "apellido")
		}
		details := map[string]any{"profile": map[string]string{"name": profile.Name, "apellido": profile.LastName}, "missing_fields": missing, "next_action": "COMPLETE_REGISTRATION_PROFILE"}
		if profile.AccountTypeRequired {
			details["next_action"] = "SELECT_ACCOUNT_TYPE"
			web.Error(c, http.StatusUnprocessableEntity, "ACCOUNT_TYPE_REQUIRED", "Elegí si la cuenta es cliente o comercio", details)
		} else {
			web.Error(c, http.StatusUnprocessableEntity, "REGISTRATION_PROFILE_REQUIRED", "Completá nombre y apellido antes de crear tu cuenta. Si tu app no muestra ambos campos, actualizá Puntazo.", details)
		}
		return
	}

	switch {
	case errors.Is(err, repository.ErrQuoteExpired):
		web.Error(c, http.StatusConflict, "QUOTE_EXPIRED", "La cotización venció. Revisá el importe actualizado.", nil)
	case errors.Is(err, repository.ErrQuoteChanged):
		web.Error(c, http.StatusConflict, "QUOTE_CHANGED", "El plan cambió. Revisá una nueva cotización.", nil)
	case errors.Is(err, service.ErrRegistrationProfileRequired):
		web.Error(c, http.StatusUnprocessableEntity, "REGISTRATION_PROFILE_REQUIRED", "Completá nombre y apellido. Actualizá Puntazo si tu aplicación no muestra ambos campos.", map[string]any{"next_action": "COMPLETE_REGISTRATION_PROFILE"})
	case errors.Is(err, repository.ErrEmailUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "EMAIL_UNAVAILABLE", "El correo no está disponible; intentá nuevamente cuando esté configurado", nil)
	case errors.Is(err, service.ErrPlacesUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Google Places no está disponible", nil)
	case errors.Is(err, service.ErrMediaTooLarge):
		web.Error(c, http.StatusRequestEntityTooLarge, "MEDIA_TOO_LARGE", "La imagen supera el máximo permitido", nil)
	case errors.Is(err, service.ErrMediaType):
		web.Error(c, http.StatusUnsupportedMediaType, "MEDIA_TYPE_UNSUPPORTED", "Formato de imagen no permitido", nil)
	case errors.Is(err, service.ErrMediaUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "MEDIA_STORAGE_UNAVAILABLE", "El almacenamiento de imágenes no está disponible", nil)
	case errors.Is(err, service.ErrEmailChangeUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "EMAIL_CHANGE_UNAVAILABLE", "El cambio de correo requiere que el envío de emails esté habilitado", nil)
	case errors.Is(err, service.ErrBillingUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "BILLING_UNAVAILABLE", "La facturación todavía no está habilitada", nil)
	case errors.Is(err, service.ErrBillingInProgress):
		web.Error(c, http.StatusConflict, "BILLING_IN_PROGRESS", "La operación no se completó: la suscripción se está preparando o verificando con Mercado Pago; intentá nuevamente más tarde", nil)
	case errors.Is(err, service.ErrTrialStartUnknown):
		web.Error(c, http.StatusConflict, "TRIAL_START_UNKNOWN", "No podemos confirmar la fecha del primer acceso. Contactá soporte antes de configurar el pago", nil)
	case errors.Is(err, service.ErrBillingRejected):
		web.Error(c, http.StatusServiceUnavailable, "BILLING_REJECTED", "Mercado Pago rechazó el checkout. Podés volver a intentarlo", nil)
	case errors.Is(err, service.ErrBillingProviderFailure):
		web.Error(c, http.StatusServiceUnavailable, "BILLING_PROVIDER_ERROR", "La operación no se completó: Mercado Pago no la confirmó; consultá el estado antes de reintentar", nil)
	case errors.Is(err, service.ErrSubscriptionExists):
		web.Error(c, http.StatusConflict, "SUBSCRIPTION_EXISTS", "La marca ya tiene una suscripción activa o pendiente", nil)
	case errors.Is(err, repository.ErrSubscriptionChangeRequired):
		web.Error(c, http.StatusConflict, "SUBSCRIPTION_CHANGE_REQUIRED", "Cancelá la suscripción antes de cambiar sucursales o eliminar la marca", nil)
	case errors.Is(err, repository.ErrReferralCodeInvalid):
		web.Error(c, http.StatusUnprocessableEntity, "REFERRAL_CODE_INVALID", "El código de referido no es válido o no está disponible", nil)
	case errors.Is(err, service.ErrReferralValidationUnavailable):
		web.Error(c, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "La validación de referidos no está disponible", nil)
	case errors.Is(err, service.ErrInvalidRequest), errors.Is(err, repository.ErrInvalidRequest):
		web.Error(c, http.StatusUnprocessableEntity, "INVALID_REQUEST", "Solicitud inválida", nil)
	case errors.Is(err, service.ErrAccountTypeRequired):
		web.Error(c, http.StatusUnprocessableEntity, "ACCOUNT_TYPE_REQUIRED", "Elegí si la cuenta es cliente o comercio", map[string]any{"next_action": "SELECT_ACCOUNT_TYPE"})
	case errors.Is(err, service.ErrInvalidCredentials):
		web.Error(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Credenciales inválidas", nil)
	case errors.Is(err, service.ErrRecentAuthRequired):
		web.Error(c, http.StatusUnauthorized, "RECENT_AUTH_REQUIRED", "Volvé a autenticarte para continuar", nil)
	case errors.Is(err, service.ErrForbidden), errors.Is(err, repository.ErrForbidden):
		web.Error(c, http.StatusForbidden, "FORBIDDEN", "Acceso denegado", nil)
	case errors.Is(err, service.ErrDemoDisabled):
		web.Error(c, http.StatusForbidden, "DEMO_SIGNUP_DISABLED", "Las altas demo están cerradas", nil)
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
	case errors.Is(err, repository.ErrProgramTypeImmutable):
		web.Error(c, http.StatusConflict, "PROGRAM_TYPE_IMMUTABLE", "El tipo de programa no puede cambiar después del primer movimiento", nil)
	case errors.Is(err, repository.ErrProgramTypeHasBenefits):
		web.Error(c, http.StatusConflict, "PROGRAM_TYPE_HAS_BENEFITS", "Eliminá los beneficios antes de cambiar el tipo de programa", nil)
	case errors.Is(err, repository.ErrSelfRoleChangeForbidden):
		web.Error(c, http.StatusConflict, "SELF_ROLE_CHANGE_FORBIDDEN", "Otro administrador o propietario debe cambiar tu acceso", nil)
	case errors.Is(err, repository.ErrAccountModeConflict):
		web.Error(c, http.StatusConflict, "ACCOUNT_MODE_CONFLICT", "La cuenta tiene actividad como cliente y no puede convertirse en personal", nil)
	case errors.Is(err, repository.ErrCampaignOverlap):
		web.Error(c, http.StatusConflict, "CAMPAIGN_OVERLAP", "Ya hay una campaña activa para alguno de los programas en esas fechas. Ajustá la ventana o pausá la otra campaña.", nil)
	case errors.Is(err, repository.ErrCampaignChanged):
		web.Error(c, http.StatusConflict, "CAMPAIGN_CHANGED", "La campaña cambió mientras la editabas. Cerrá el formulario y volvé a abrir Editar para revisar la versión actual.", nil)
	case errors.Is(err, repository.ErrConflict):
		web.Error(c, http.StatusConflict, "RESOURCE_CONFLICT", "La operación viola una invariante activa", nil)
	case errors.Is(err, service.ErrIdentityToken):
		web.Error(c, http.StatusUnprocessableEntity, "IDENTITY_TOKEN_INVALID", "Token inválido, vencido o utilizado", nil)
	case errors.Is(err, repository.ErrInvitationEmailRegistered):
		web.Error(c, http.StatusConflict, "INVITATION_EMAIL_ALREADY_REGISTERED", "Este correo ya está registrado. Usá otro correo para invitar al personal", nil)
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

package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound                = errors.New("not found")
	ErrEmailExists             = errors.New("email exists")
	ErrIdempotencyConflict     = errors.New("idempotency conflict")
	ErrIdempotencyInProgress   = errors.New("idempotency in progress")
	ErrPreviewExpired          = errors.New("preview expired")
	ErrPreviewConsumed         = errors.New("preview consumed")
	ErrPreviewChanged          = errors.New("preview changed")
	ErrInsufficientBalance     = errors.New("insufficient balance")
	ErrInvalidRequest          = errors.New("invalid request")
	ErrForbidden               = errors.New("forbidden")
	ErrSessionReuse            = errors.New("session refresh reuse")
	ErrPreconditionFailed      = errors.New("precondition failed")
	ErrOwnershipTransfer       = errors.New("ownership transfer required")
	ErrConflict                = errors.New("resource conflict")
	ErrProgramTypeImmutable    = errors.New("program type immutable")
	ErrProgramTypeHasBenefits  = errors.New("program type has benefits")
	ErrSelfRoleChangeForbidden = errors.New("self role change forbidden")
	ErrAccountModeConflict     = errors.New("account mode conflict")
	ErrSignupDisabled          = errors.New("signup disabled")
)

func normalize(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if pgErr.ConstraintName == "usuarios_email_key" {
			return ErrEmailExists
		}
		if pgErr.ConstraintName == "invitaciones_marca_email_pendiente_uk" {
			return ErrConflict
		}
	}
	return err
}

func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

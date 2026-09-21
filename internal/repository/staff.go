package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"clientesFrecuentes/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvitationInvalid         = errors.New("invitation invalid")
	ErrInvitationEmailRegistered = errors.New("invitation email already registered")
)

func requireStaffManager(ctx context.Context, tx pgx.Tx, actorID, brandID int64) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo AND rol IN ('PROPIETARIO','ADMINISTRADOR'))`, actorID, brandID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func scanInvitation(row pgx.Row) (model.BrandInvitation, error) {
	var invitation model.BrandInvitation
	err := row.Scan(&invitation.ID, &invitation.BrandID, &invitation.Email, &invitation.Role, &invitation.BranchIDs, &invitation.Status, &invitation.ExpiresAt, &invitation.Version, &invitation.CreatedAt)
	return invitation, err
}

func expireInvitations(ctx context.Context, tx pgx.Tx, brandID int64, now time.Time) error {
	_, err := tx.Exec(ctx, `WITH expired AS (
		UPDATE invitaciones_marca SET estado='EXPIRADA',version=version+1,updated_at=$2
		WHERE marca_id=$1 AND estado='PENDIENTE' AND expires_at<=$2 RETURNING id
	) UPDATE email_outbox SET estado='FAILED',ultimo_error='invitation expired',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL
	WHERE invitation_id IN(SELECT id FROM expired) AND estado IN('PENDING','SENDING')`, brandID, now)
	return err
}

const invitationSelect = `SELECT i.id::text,i.marca_id,i.email::text,i.rol,COALESCE(array_agg(s.sucursal_id ORDER BY s.sucursal_id) FILTER(WHERE s.sucursal_id IS NOT NULL),'{}'),i.estado,i.expires_at,i.version,i.created_at FROM invitaciones_marca i LEFT JOIN invitaciones_sucursales s ON s.invitacion_id=i.id`

func (r *Repository) ListInvitations(ctx context.Context, actorID, brandID int64) ([]model.BrandInvitation, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = requireStaffManager(ctx, tx, actorID, brandID); err != nil {
		return nil, err
	}
	if err = expireInvitations(ctx, tx, brandID, r.Now().UTC()); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, invitationSelect+` WHERE i.marca_id=$1 GROUP BY i.id ORDER BY i.created_at DESC`, brandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.BrandInvitation, 0)
	for rows.Next() {
		item, scanErr := scanInvitation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

func enqueueInvitationEmail(ctx context.Context, tx pgx.Tx, key []byte, inviterID int64, invitationID uuid.UUID, email, token string, expires time.Time) error {
	outboxID := uuid.New()
	ciphertext, nonce, err := encryptOutboxToken(token, outboxID.String(), key)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO email_outbox(id,usuario_id,tipo,destinatario,asunto,token_ciphertext,token_nonce,token_expires_at,invitation_id) VALUES($1,$2,'BRAND_INVITATION',$3,$4,$5,$6,$7,$8)`, outboxID, inviterID, email, "Te invitaron a una marca en Puntazo", ciphertext, nonce, expires, invitationID)
	return err
}

func validateInvitationBranches(ctx context.Context, tx pgx.Tx, brandID int64, branchIDs []int64) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sucursales WHERE marca_id=$1 AND activo AND deleted_at IS NULL AND id=ANY($2)`, brandID, branchIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(branchIDs) {
		return ErrInvalidRequest
	}
	return nil
}

func (r *Repository) CreateInvitation(ctx context.Context, actorID, brandID int64, key string, fingerprint []byte, req model.CreateInvitationRequest, token string, hash []byte, expires time.Time, build func(model.BrandInvitation) ([]byte, error)) (IdempotentResult, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IdempotentResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = requireStaffManager(ctx, tx, actorID, brandID); err != nil {
		return IdempotentResult{}, err
	}
	claimed, err := claimIdempotency(ctx, tx, key, "brand:"+strconv.FormatInt(brandID, 10)+":actor:"+strconv.FormatInt(actorID, 10), "CREATE_BRAND_INVITATION", fingerprint)
	if err != nil {
		return IdempotentResult{}, err
	}
	if claimed != nil {
		return *claimed, nil
	}
	if err = expireInvitations(ctx, tx, brandID, r.Now().UTC()); err != nil {
		return IdempotentResult{}, err
	}
	if err = validateInvitationBranches(ctx, tx, brandID, req.BranchIDs); err != nil {
		return IdempotentResult{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usuarios WHERE email=$1)`, req.Email).Scan(&exists); err != nil {
		return IdempotentResult{}, err
	}
	if exists {
		return IdempotentResult{}, ErrInvitationEmailRegistered
	}
	id := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO invitaciones_marca(id,marca_id,invitado_por,email,rol,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, brandID, actorID, req.Email, req.Role, hash, expires); err != nil {
		return IdempotentResult{}, normalize(err)
	}
	for _, branchID := range req.BranchIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO invitaciones_sucursales(invitacion_id,sucursal_id,marca_id) VALUES($1,$2,$3)`, id, branchID, brandID); err != nil {
			return IdempotentResult{}, err
		}
	}
	if err = enqueueInvitationEmail(ctx, tx, r.OutboxCipherKey, actorID, id, req.Email, token, expires); err != nil {
		return IdempotentResult{}, err
	}
	item, err := scanInvitation(tx.QueryRow(ctx, invitationSelect+` WHERE i.id=$1 GROUP BY i.id`, id))
	if err != nil {
		return IdempotentResult{}, err
	}
	body, err := build(item)
	if err != nil {
		return IdempotentResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE solicitudes_idempotentes SET estado='COMPLETED',response_status=201,response_body=$2,completed_at=now() WHERE idempotency_key=$1`, key, body); err != nil {
		return IdempotentResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IdempotentResult{}, normalize(err)
	}
	return IdempotentResult{Status: 201, Body: body}, nil
}

func (r *Repository) RevokeInvitation(ctx context.Context, actorID, brandID int64, id string) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = requireStaffManager(ctx, tx, actorID, brandID); err != nil {
		return err
	}
	if err = expireInvitations(ctx, tx, brandID, r.Now().UTC()); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE invitaciones_marca SET estado='REVOCADA',version=version+1,updated_at=now() WHERE id=$1 AND marca_id=$2 AND estado='PENDIENTE'`, id, brandID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrInvitationInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE email_outbox SET estado='FAILED',ultimo_error='invitation revoked',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL WHERE invitation_id=$1 AND estado IN('PENDING','SENDING')`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) ResendInvitation(ctx context.Context, actorID, brandID int64, id, token string, hash []byte, expires time.Time) (model.BrandInvitation, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.BrandInvitation{}, err
	}
	defer tx.Rollback(ctx)
	if err = requireStaffManager(ctx, tx, actorID, brandID); err != nil {
		return model.BrandInvitation{}, err
	}
	if err = expireInvitations(ctx, tx, brandID, r.Now().UTC()); err != nil {
		return model.BrandInvitation{}, err
	}
	var email string
	if err = tx.QueryRow(ctx, `UPDATE invitaciones_marca SET token_hash=$3,expires_at=$4,version=version+1,updated_at=now() WHERE id=$1 AND marca_id=$2 AND estado='PENDIENTE' RETURNING email::text`, id, brandID, hash, expires).Scan(&email); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.BrandInvitation{}, ErrInvitationInvalid
		}
		return model.BrandInvitation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE email_outbox SET estado='FAILED',ultimo_error='superseded',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL WHERE invitation_id=$1 AND estado IN('PENDING','SENDING')`, id); err != nil {
		return model.BrandInvitation{}, err
	}
	if err = enqueueInvitationEmail(ctx, tx, r.OutboxCipherKey, actorID, uuid.MustParse(id), email, token, expires); err != nil {
		return model.BrandInvitation{}, err
	}
	item, err := scanInvitation(tx.QueryRow(ctx, invitationSelect+` WHERE i.id=$1 GROUP BY i.id`, id))
	if err != nil {
		return item, err
	}
	return item, tx.Commit(ctx)
}

func (r *Repository) PublicInvitation(ctx context.Context, hash []byte, now time.Time) (model.PublicInvitation, error) {
	var item model.PublicInvitation
	var email string
	err := r.Pool.QueryRow(ctx, `SELECT m.nombre,i.email::text,i.rol,i.expires_at FROM invitaciones_marca i JOIN marcas m ON m.id=i.marca_id AND m.activo WHERE i.token_hash=$1 AND i.estado='PENDIENTE' AND i.expires_at>$2`, hash, now).Scan(&item.BrandName, &email, &item.Role, &item.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrInvitationInvalid
	}
	item.MaskedEmail = maskEmail(email)
	return item, err
}

func maskEmail(email string) string {
	for i, char := range email {
		if char == '@' {
			if i <= 1 {
				return "*" + email[i:]
			}
			return email[:1] + "***" + email[i:]
		}
	}
	return "***"
}

func (r *Repository) RegisterInvitation(ctx context.Context, hash []byte, now time.Time, name, passwordHash string) (model.User, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	var invitationID uuid.UUID
	var brandID int64
	var email, role string
	err = tx.QueryRow(ctx, `SELECT i.id,i.marca_id,i.email::text,i.rol FROM invitaciones_marca i JOIN marcas m ON m.id=i.marca_id AND m.activo AND m.deleted_at IS NULL WHERE i.token_hash=$1 AND i.estado='PENDIENTE' AND i.expires_at>$2 FOR UPDATE OF i,m`, hash, now).Scan(&invitationID, &brandID, &email, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrInvitationInvalid
	}
	if err != nil {
		return model.User{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usuarios WHERE email=$1)`, email).Scan(&exists); err != nil {
		return model.User{}, err
	}
	if exists {
		return model.User{}, ErrEmailExists
	}
	var user model.User
	err = tx.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta,qr_hash,email_verified_at)
		VALUES($1,$2,$3,'PERSONAL_MARCA',NULL,$4)
		RETURNING id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,true,auth_version,version,created_at`, email, passwordHash, name, now).
		Scan(&user.ID, &user.Email, &user.Name, &user.LastName, &user.Alias, &user.PhotoURL, &user.AccountType, &user.Active, &user.EmailVerified, &user.AuthVersion, &user.Version, &user.CreatedAt)
	if err != nil {
		return model.User{}, normalize(err)
	}
	var membershipID int64
	if err = tx.QueryRow(ctx, `INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,$3) RETURNING id`, user.ID, brandID, role).Scan(&membershipID); err != nil {
		return model.User{}, normalize(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO membresias_sucursales(membresia_id,sucursal_id,marca_id) SELECT $1,sucursal_id,marca_id FROM invitaciones_sucursales WHERE invitacion_id=$2`, membershipID, invitationID); err != nil {
		return model.User{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE invitaciones_marca SET estado='ACEPTADA',accepted_by=$2,accepted_at=$3,version=version+1,updated_at=$3 WHERE id=$1`, invitationID, user.ID, now); err != nil {
		return model.User{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE email_outbox SET estado='FAILED',ultimo_error='invitation accepted',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL WHERE invitation_id=$1 AND estado IN('PENDING','SENDING')`, invitationID); err != nil {
		return model.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.User{}, normalize(err)
	}
	return user, nil
}

func (r *Repository) staffMemberByID(ctx context.Context, brandID, membershipID int64) (model.StaffMember, error) {
	var item model.StaffMember
	err := r.Pool.QueryRow(ctx, `SELECT target.id,u.id,u.email::text,u.nombre,target.rol,COALESCE(array_agg(ms.sucursal_id ORDER BY ms.sucursal_id) FILTER(WHERE ms.activo),'{}'),target.activo,target.version FROM membresias_marca target JOIN usuarios u ON u.id=target.usuario_id LEFT JOIN membresias_sucursales ms ON ms.membresia_id=target.id WHERE target.marca_id=$1 AND target.id=$2 GROUP BY target.id,u.id`, brandID, membershipID).Scan(&item.MembershipID, &item.UserID, &item.Email, &item.Name, &item.Role, &item.BranchIDs, &item.Active, &item.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (r *Repository) GetStaffMember(ctx context.Context, actorID, brandID, membershipID int64) (model.StaffMember, error) {
	var item model.StaffMember
	err := r.Pool.QueryRow(ctx, `SELECT target.id,u.id,u.email::text,u.nombre,target.rol,COALESCE(array_agg(ms.sucursal_id ORDER BY ms.sucursal_id) FILTER(WHERE ms.activo),'{}'),target.activo,target.version FROM membresias_marca actor JOIN membresias_marca target ON target.marca_id=actor.marca_id JOIN usuarios u ON u.id=target.usuario_id LEFT JOIN membresias_sucursales ms ON ms.membresia_id=target.id WHERE actor.usuario_id=$1 AND actor.marca_id=$2 AND actor.activo AND actor.rol IN ('PROPIETARIO','ADMINISTRADOR') AND target.id=$3 GROUP BY target.id,u.id`, actorID, brandID, membershipID).Scan(&item.MembershipID, &item.UserID, &item.Email, &item.Name, &item.Role, &item.BranchIDs, &item.Active, &item.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (r *Repository) ListStaff(ctx context.Context, actorID, brandID int64) ([]model.StaffMember, error) {
	rows, err := r.Pool.Query(ctx, `SELECT target.id,u.id,u.email::text,u.nombre,target.rol,COALESCE(array_agg(ms.sucursal_id ORDER BY ms.sucursal_id) FILTER(WHERE ms.activo),'{}'),target.activo,target.version FROM membresias_marca actor JOIN membresias_marca target ON target.marca_id=actor.marca_id JOIN usuarios u ON u.id=target.usuario_id LEFT JOIN membresias_sucursales ms ON ms.membresia_id=target.id WHERE actor.usuario_id=$1 AND actor.marca_id=$2 AND actor.activo AND actor.rol IN ('PROPIETARIO','ADMINISTRADOR') GROUP BY target.id,u.id ORDER BY u.nombre`, actorID, brandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.StaffMember, 0)
	for rows.Next() {
		var x model.StaffMember
		if err = rows.Scan(&x.MembershipID, &x.UserID, &x.Email, &x.Name, &x.Role, &x.BranchIDs, &x.Active, &x.Version); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}

func (r *Repository) UpdateStaff(ctx context.Context, actorID, brandID, membershipID int64, version int, req model.UpdateStaffRequest) (model.StaffMember, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.StaffMember{}, err
	}
	defer tx.Rollback(ctx)
	if err = requireStaffManager(ctx, tx, actorID, brandID); err != nil {
		return model.StaffMember{}, err
	}
	var currentRole string
	var targetUserID int64
	if err = tx.QueryRow(ctx, `SELECT rol,usuario_id FROM membresias_marca WHERE id=$1 AND marca_id=$2 AND activo FOR UPDATE`, membershipID, brandID).Scan(&currentRole, &targetUserID); errors.Is(err, pgx.ErrNoRows) {
		return model.StaffMember{}, ErrNotFound
	} else if err != nil {
		return model.StaffMember{}, err
	}
	if currentRole == "PROPIETARIO" {
		return model.StaffMember{}, ErrForbidden
	}
	if targetUserID == actorID {
		return model.StaffMember{}, ErrSelfRoleChangeForbidden
	}
	role := currentRole
	if req.Role != nil {
		role = *req.Role
	}
	tag, err := tx.Exec(ctx, `UPDATE membresias_marca SET rol=$4,version=version+1,updated_at=now() WHERE id=$1 AND marca_id=$2 AND version=$3 AND activo`, membershipID, brandID, version, role)
	if err != nil {
		return model.StaffMember{}, err
	}
	if tag.RowsAffected() != 1 {
		return model.StaffMember{}, ErrPreconditionFailed
	}
	if role == "ADMINISTRADOR" {
		if req.BranchIDs != nil && len(*req.BranchIDs) != 0 {
			return model.StaffMember{}, ErrInvalidRequest
		}
		if _, err = tx.Exec(ctx, `UPDATE membresias_sucursales SET activo=false WHERE membresia_id=$1 AND activo`, membershipID); err != nil {
			return model.StaffMember{}, err
		}
	} else if req.BranchIDs != nil {
		if err = validateInvitationBranches(ctx, tx, brandID, *req.BranchIDs); err != nil {
			return model.StaffMember{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE membresias_sucursales SET activo=false WHERE membresia_id=$1`, membershipID); err != nil {
			return model.StaffMember{}, err
		}
		for _, branchID := range *req.BranchIDs {
			if _, err = tx.Exec(ctx, `INSERT INTO membresias_sucursales(membresia_id,sucursal_id,marca_id,activo) VALUES($1,$2,$3,true) ON CONFLICT(membresia_id,sucursal_id) DO UPDATE SET activo=true,marca_id=EXCLUDED.marca_id`, membershipID, branchID, brandID); err != nil {
				return model.StaffMember{}, err
			}
		}
	}
	if role == "OPERADOR" {
		var assigned int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM membresias_sucursales WHERE membresia_id=$1 AND activo`, membershipID).Scan(&assigned); err != nil {
			return model.StaffMember{}, err
		}
		if assigned == 0 {
			return model.StaffMember{}, ErrInvalidRequest
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return model.StaffMember{}, err
	}
	return r.GetStaffMember(ctx, actorID, brandID, membershipID)
}

func (r *Repository) DeleteStaff(ctx context.Context, actorID, brandID, membershipID int64, version int) error {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = requireStaffManager(ctx, tx, actorID, brandID); err != nil {
		return err
	}
	var currentRole string
	var currentVersion int
	var targetUserID int64
	if err = tx.QueryRow(ctx, `SELECT rol,version,usuario_id FROM membresias_marca WHERE id=$1 AND marca_id=$2 AND activo FOR UPDATE`, membershipID, brandID).Scan(&currentRole, &currentVersion, &targetUserID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if currentRole == "PROPIETARIO" {
		return ErrForbidden
	}
	if targetUserID == actorID {
		return ErrSelfRoleChangeForbidden
	}
	if currentVersion != version {
		return ErrPreconditionFailed
	}
	tag, err := tx.Exec(ctx, `UPDATE membresias_marca SET activo=false,version=version+1,updated_at=now() WHERE id=$1 AND marca_id=$2 AND version=$3 AND activo AND rol<>'PROPIETARIO'`, membershipID, brandID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrPreconditionFailed
	}
	if _, err = tx.Exec(ctx, `UPDATE membresias_sucursales SET activo=false WHERE membresia_id=$1`, membershipID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

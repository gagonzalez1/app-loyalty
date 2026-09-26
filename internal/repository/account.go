package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"clientesFrecuentes/internal/model"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) ExportAccount(ctx context.Context, id int64) (model.AccountExport, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return model.AccountExport{}, err
	}
	defer tx.Rollback(ctx)

	var out model.AccountExport
	err = tx.QueryRow(ctx, `SELECT now(),id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,(email_verified_at IS NOT NULL),auth_version,version,created_at FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL`, id).
		Scan(&out.ExportedAt, &out.User.ID, &out.User.Email, &out.User.Name, &out.User.LastName, &out.User.Alias, &out.User.PhotoURL, &out.User.AccountType, &out.User.Active, &out.User.EmailVerified, &out.User.AuthVersion, &out.User.Version, &out.User.CreatedAt)
	if err != nil {
		return model.AccountExport{}, err
	}

	rows, err := tx.Query(ctx, `SELECT m.id,m.nombre,mm.rol,COALESCE(array_agg(ms.sucursal_id ORDER BY ms.sucursal_id) FILTER(WHERE ms.activo),'{}'),mm.activo
		FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id
		LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id
		WHERE mm.usuario_id=$1 GROUP BY m.id,m.nombre,mm.rol,mm.activo ORDER BY m.id`, id)
	if err != nil {
		return model.AccountExport{}, err
	}
	out.Memberships = make([]model.Membership, 0)
	for rows.Next() {
		var membership model.Membership
		if err = rows.Scan(&membership.BrandID, &membership.BrandName, &membership.Role, &membership.BranchIDs, &membership.Active); err != nil {
			rows.Close()
			return model.AccountExport{}, err
		}
		out.Memberships = append(out.Memberships, membership)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return model.AccountExport{}, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT t.id,t.marca_id,m.nombre,t.saldo_sellos,t.saldo_puntos,t.activo,t.version,t.created_at
		FROM tarjetas t JOIN marcas m ON m.id=t.marca_id WHERE t.usuario_id=$1 ORDER BY t.id`, id)
	if err != nil {
		return model.AccountExport{}, err
	}
	out.Cards = make([]model.AccountExportCard, 0)
	for rows.Next() {
		var card model.AccountExportCard
		if err = rows.Scan(&card.ID, &card.BrandID, &card.BrandName, &card.BalanceStamps, &card.BalancePoints, &card.Active, &card.Version, &card.CreatedAt); err != nil {
			rows.Close()
			return model.AccountExport{}, err
		}
		out.Cards = append(out.Cards, card)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return model.AccountExport{}, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT h.id,h.operation_id,h.tarjeta_id,h.marca_id,h.marca_nombre_snapshot,h.sucursal_id,h.sucursal_nombre_snapshot,h.operacion,h.programa_tipo,h.programa_id_snapshot,h.sentido,h.cantidad,h.saldo_anterior,h.saldo_posterior,h.beneficio_nombre_snapshot,h.beneficio_requisito_snapshot,h.beneficio_requisito_puntos_snapshot,h.occurred_at
		FROM historial_movimientos h JOIN tarjetas t ON t.id=h.tarjeta_id
		WHERE t.usuario_id=$1 OR h.usuario_operador_id=$1 ORDER BY h.occurred_at,h.id`, id)
	if err != nil {
		return model.AccountExport{}, err
	}
	out.Movements = make([]model.Movement, 0)
	for rows.Next() {
		var movement model.Movement
		if err = rows.Scan(&movement.ID, &movement.OperationID, &movement.CardID, &movement.BrandID, &movement.BrandName, &movement.BranchID, &movement.BranchName, &movement.Operation, &movement.ProgramType, &movement.ProgramIDSnapshot, &movement.Direction, &movement.Amount, &movement.BalanceBefore, &movement.BalanceAfter, &movement.BenefitNameSnapshot, &movement.BenefitRequiredStampsSnapshot, &movement.BenefitRequiredPointsSnapshot, &movement.OccurredAt); err != nil {
			rows.Close()
			return model.AccountExport{}, err
		}
		out.Movements = append(out.Movements, movement)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return model.AccountExport{}, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return model.AccountExport{}, err
	}
	return out, nil
}

func (r *Repository) AnonymizeAccount(ctx context.Context, id int64, expectedVersion int) (time.Time, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)
	var currentVersion int
	var currentEmail, currentName string
	if err = tx.QueryRow(ctx, `SELECT version,email::text,nombre FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL FOR UPDATE`, id).Scan(&currentVersion, &currentEmail, &currentName); err != nil {
		return time.Time{}, err
	}
	if currentVersion != expectedVersion {
		return time.Time{}, ErrPreconditionFailed
	}
	_ = currentName
	var blocksOwnership bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM membresias_marca mine
		WHERE mine.usuario_id=$1 AND mine.activo AND mine.rol='PROPIETARIO'
		AND NOT EXISTS(SELECT 1 FROM membresias_marca other WHERE other.marca_id=mine.marca_id AND other.usuario_id<>$1 AND other.activo AND other.rol='PROPIETARIO')
	)`, id).Scan(&blocksOwnership)
	if err != nil {
		return time.Time{}, err
	}
	if blocksOwnership {
		return time.Time{}, ErrOwnershipTransfer
	}
	var deletedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&deletedAt); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE membresias_sucursales SET activo=false WHERE membresia_id IN (SELECT id FROM membresias_marca WHERE usuario_id=$1)`, id); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE membresias_marca SET activo=false WHERE usuario_id=$1`, id); err != nil {
		return time.Time{}, err
	}
	statements := []string{
		`UPDATE tarjetas SET activo=false,deleted_at=COALESCE(deleted_at,$2),version=version+1 WHERE usuario_id=$1 AND activo`,
		`UPDATE previews_movimiento SET expires_at=LEAST(expires_at,$2) WHERE (actor_id=$1 OR cliente_id=$1) AND consumed_at IS NULL`,
		`UPDATE sesiones_auth SET revoked_at=COALESCE(revoked_at,$2) WHERE usuario_id=$1`,
		`UPDATE tokens_identidad_email SET consumed_at=COALESCE(consumed_at,$2) WHERE usuario_id=$1`,
	}
	for _, statement := range statements {
		if _, err = tx.Exec(ctx, statement, id, deletedAt); err != nil {
			return time.Time{}, err
		}
	}
	// Account deletion revokes device destinations and cascades queued pushes.
	if _, err = tx.Exec(ctx, `DELETE FROM push_tokens WHERE usuario_id=$1`, id); err != nil {
		return time.Time{}, err
	}
	tombstone := fmt.Sprintf("deleted-%d@anon.invalid", id)
	if _, err = tx.Exec(ctx, `UPDATE email_outbox SET destinatario=$2,estado=CASE WHEN estado IN ('PENDING','SENDING') THEN 'FAILED' ELSE estado END,cuerpo_texto=NULL,cuerpo_html=NULL,ultimo_error=NULL,lease_until=NULL,lease_owner=NULL,token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,disponible_at=$3 WHERE usuario_id=$1 OR destinatario=$4 OR invitation_id IN(SELECT id FROM invitaciones_marca WHERE accepted_by=$1)`, id, tombstone, deletedAt, currentEmail); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE invitaciones_marca SET email=$2,estado=CASE WHEN estado='PENDIENTE' THEN 'REVOCADA' ELSE estado END,version=version+1,updated_at=$3 WHERE accepted_by=$1 OR email=$4`, id, tombstone, deletedAt, currentEmail); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE solicitudes_idempotentes SET actor_scope=CASE WHEN actor_scope=$2 OR actor_scope=$3 THEN $1 ELSE actor_scope END,fingerprint=decode(repeat('00',32),'hex'),response_body=CASE WHEN estado='COMPLETED' THEN '{}'::bytea ELSE NULL END WHERE actor_scope=$2 OR actor_scope=$3 OR strpos(convert_from(COALESCE(response_body,''::bytea),'UTF8'),$4)>0`, "deleted-user:"+strconv.FormatInt(id, 10), "demo-email:"+currentEmail, "user:"+strconv.FormatInt(id, 10), currentEmail); err != nil {
		return time.Time{}, err
	}
	command, err := tx.Exec(ctx, `UPDATE usuarios SET email=('deleted-' || id || '@anon.invalid')::citext,password_hash=NULL,google_id=NULL,nombre='Cuenta anonimizada',apellido=NULL,alias=NULL,foto_url=NULL,qr_hash=NULL,activo=false,email_verified_at=NULL,auth_version=auth_version+1,version=version+1,deleted_at=$2 WHERE id=$1 AND version=$3`, id, deletedAt, expectedVersion)
	if err != nil {
		return time.Time{}, err
	}
	if command.RowsAffected() != 1 {
		return time.Time{}, ErrPreconditionFailed
	}
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, err
	}
	return deletedAt, nil
}

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
	err = tx.QueryRow(ctx, `SELECT now(),id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,(email_verified_at IS NOT NULL),auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0')) FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL`, id).
		Scan(&out.ExportedAt, &out.User.ID, &out.User.Email, &out.User.Name, &out.User.LastName, &out.User.Alias, &out.User.PhotoURL, &out.User.AccountType, &out.User.Active, &out.User.EmailVerified, &out.User.AuthVersion, &out.User.Version, &out.User.CreatedAt, &out.User.UserCode)
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
	out.ReviewProgress = []model.ReviewProgressExport{}
	rows, err = tx.Query(ctx, `SELECT sucursal_id,purchases FROM progreso_resenas WHERE usuario_id=$1 ORDER BY sucursal_id`, id)
	if err != nil {
		return model.AccountExport{}, err
	}
	for rows.Next() {
		var progress model.ReviewProgressExport
		if err = rows.Scan(&progress.BranchID, &progress.Purchases); err != nil {
			rows.Close()
			return model.AccountExport{}, err
		}
		out.ReviewProgress = append(out.ReviewProgress, progress)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return model.AccountExport{}, err
	}
	rows.Close()
	out.ReviewInvitations = []model.ReviewInvitationExport{}
	rows, err = tx.Query(ctx, `SELECT id::text,sucursal_id,tarjeta_id,operation_id::text,occurred_at,cancelled_at,shown_at,skipped_at,clicked_at FROM invitaciones_resenas WHERE usuario_id=$1 ORDER BY occurred_at,id`, id)
	if err != nil {
		return model.AccountExport{}, err
	}
	for rows.Next() {
		var invitation model.ReviewInvitationExport
		if err = rows.Scan(&invitation.ID, &invitation.BranchID, &invitation.CardID, &invitation.OperationID, &invitation.OccurredAt, &invitation.CancelledAt, &invitation.ShownAt, &invitation.SkippedAt, &invitation.ClickedAt); err != nil {
			rows.Close()
			return model.AccountExport{}, err
		}
		out.ReviewInvitations = append(out.ReviewInvitations, invitation)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return model.AccountExport{}, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT sucursal_id FROM sucursales_favoritas WHERE usuario_id=$1 ORDER BY sucursal_id`, id)
	if err != nil {
		return model.AccountExport{}, err
	}
	out.FavoriteBranchIDs = []int64{}
	for rows.Next() {
		var branchID int64
		if err = rows.Scan(&branchID); err != nil {
			rows.Close()
			return model.AccountExport{}, err
		}
		out.FavoriteBranchIDs = append(out.FavoriteBranchIDs, branchID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return model.AccountExport{}, err
	}
	rows.Close()
	var apple model.AppleIdentityExport
	err = tx.QueryRow(ctx, `SELECT subject,email::text,private_relay,created_at FROM apple_identities WHERE usuario_id=$1`, id).Scan(&apple.Subject, &apple.Email, &apple.PrivateRelay, &apple.LinkedAt)
	if err != nil && err != pgx.ErrNoRows {
		return model.AccountExport{}, err
	}
	if err == nil {
		out.AppleIdentity = &apple
	}
	if err = tx.Commit(ctx); err != nil {
		return model.AccountExport{}, err
	}
	return out, nil
}

// Subscription cancellation runs before local deletion while the affected brand
// and subscription rows are locked. Its provider operation must be safe to retry
// if PostgreSQL rejects the serializable transaction after the external call.
func (r *Repository) AnonymizeAccount(ctx context.Context, id int64, expectedVersion int, cancel ...func(context.Context, []SubscriptionRecord) error) (time.Time, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)
	var currentVersion int
	var currentEmail string
	if err = tx.QueryRow(ctx, `SELECT version,email::text FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL FOR UPDATE`, id).Scan(&currentVersion, &currentEmail); err != nil {
		return time.Time{}, err
	}
	if currentVersion != expectedVersion {
		return time.Time{}, ErrPreconditionFailed
	}
	// Match checkout's brand lock. Sort all brands before deciding which must
	// close, so simultaneous departures from a shared brand cannot orphan it.
	rows, err := tx.Query(ctx, `SELECT m.id FROM marcas m JOIN membresias_marca mine ON mine.marca_id=m.id
		WHERE mine.usuario_id=$1 AND mine.activo AND mine.rol='PROPIETARIO' ORDER BY m.id FOR UPDATE OF m`, id)
	if err != nil {
		return time.Time{}, err
	}
	ownedBrands, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return time.Time{}, err
	}
	closedBrands := make([]int64, 0, len(ownedBrands))
	for _, brandID := range ownedBrands {
		var anotherOwner bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM membresias_marca other JOIN usuarios u ON u.id=other.usuario_id
			WHERE other.marca_id=$1 AND other.usuario_id<>$2 AND other.activo AND other.rol='PROPIETARIO' AND u.activo AND u.deleted_at IS NULL)`, brandID, id).Scan(&anotherOwner)
		if err != nil {
			return time.Time{}, err
		}
		if !anotherOwner {
			closedBrands = append(closedBrands, brandID)
		}
	}
	rows, err = tx.Query(ctx, `SELECT marca_id,estado,COALESCE(proveedor_suscripcion_id,''),referencia_externa
		FROM suscripciones_marca WHERE marca_id=ANY($1) AND estado<>'CANCELLED' ORDER BY marca_id FOR UPDATE`, closedBrands)
	if err != nil {
		return time.Time{}, err
	}
	subscriptions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SubscriptionRecord, error) {
		var record SubscriptionRecord
		err := row.Scan(&record.Subscription.BrandID, &record.Subscription.Status, &record.ProviderID, &record.ExternalReference)
		return record, err
	})
	if err != nil {
		return time.Time{}, err
	}
	if len(subscriptions) > 0 {
		if len(cancel) == 0 || cancel[0] == nil {
			return time.Time{}, ErrSubscriptionChangeRequired
		}
		if err = cancel[0](ctx, subscriptions); err != nil {
			return time.Time{}, err
		}
	}
	for _, record := range subscriptions {
		if _, err = tx.Exec(ctx, `UPDATE suscripciones_marca SET estado='CANCELLED',checkout_url=NULL,proximo_cobro_at=NULL,updated_at=now() WHERE marca_id=$1`, record.Subscription.BrandID); err != nil {
			return time.Time{}, err
		}
	}
	var deletedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&deletedAt); err != nil {
		return time.Time{}, err
	}
	if err = queueAppleIdentityDeletion(ctx, tx, id, deletedAt); err != nil {
		return time.Time{}, err
	}
	for _, statement := range []string{
		`UPDATE marcas SET activo=false,deleted_at=COALESCE(deleted_at,$2),version=version+1,updated_at=$2 WHERE id=ANY($1)`,
		`UPDATE sucursales SET activo=false,deleted_at=COALESCE(deleted_at,$2),version=version+1,updated_at=$2 WHERE marca_id=ANY($1) AND activo`,
		`UPDATE programas_fidelidad SET activo=false,version=version+1,updated_at=$2 WHERE marca_id=ANY($1) AND activo`,
		`UPDATE beneficios SET activo=false,deleted_at=COALESCE(deleted_at,$2),version=version+1,updated_at=$2 WHERE programa_id IN(SELECT id FROM programas_fidelidad WHERE marca_id=ANY($1)) AND activo`,
		`UPDATE tarjetas SET activo=false,deleted_at=COALESCE(deleted_at,$2),version=version+1 WHERE marca_id=ANY($1) AND activo`,
		`UPDATE previews_movimiento SET expires_at=LEAST(expires_at,$2) WHERE marca_id=ANY($1) AND consumed_at IS NULL`,
		`UPDATE membresias_sucursales SET activo=false WHERE membresia_id IN(SELECT id FROM membresias_marca WHERE marca_id=ANY($1)) AND $2::timestamptz IS NOT NULL`,
		`UPDATE membresias_marca SET activo=false,version=version+1,updated_at=$2 WHERE marca_id=ANY($1) AND activo`,
		`UPDATE accesos_demo SET activo=false WHERE marca_id=ANY($1) AND $2::timestamptz IS NOT NULL`,
		`UPDATE referral_codes SET active=false WHERE source_brand_id=ANY($1) AND $2::timestamptz IS NOT NULL`,
		`UPDATE archivos_marca SET estado='DELETE_PENDING',delete_after=$2::timestamptz+interval '24 hours',lease_owner=NULL,lease_until=NULL,version=version+1,updated_at=$2 WHERE marca_id=ANY($1) AND estado IN ('ACTIVA','UPLOAD_PENDING','UPLOAD_FAILED')`,
		`UPDATE email_outbox SET estado='FAILED',ultimo_error='brand deleted',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL WHERE invitation_id IN(SELECT id FROM invitaciones_marca WHERE marca_id=ANY($1) AND estado='PENDIENTE') AND estado IN('PENDING','SENDING') AND $2::timestamptz IS NOT NULL`,
		`UPDATE invitaciones_marca SET estado='REVOCADA',version=version+1,updated_at=$2 WHERE marca_id=ANY($1) AND estado='PENDIENTE'`,
	} {
		if _, err = tx.Exec(ctx, statement, closedBrands, deletedAt); err != nil {
			return time.Time{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE membresias_sucursales SET activo=false WHERE membresia_id IN (SELECT id FROM membresias_marca WHERE usuario_id=$1)`, id); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE membresias_marca SET activo=false,version=version+1,updated_at=now() WHERE usuario_id=$1 AND activo`, id); err != nil {
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
	// Retain anonymous branch metrics, remove links to the user's loyalty ledger.
	if _, err = tx.Exec(ctx, `UPDATE invitaciones_resenas SET usuario_id=NULL,tarjeta_id=NULL,operation_id=NULL,reservation_token=NULL,lease_until=NULL,cancelled_at=COALESCE(cancelled_at,$2) WHERE usuario_id=$1`, id, deletedAt); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM progreso_resenas WHERE usuario_id=$1`, id); err != nil {
		return time.Time{}, err
	}
	// Favorites are personal preferences and must not survive account deletion.
	if _, err = tx.Exec(ctx, `DELETE FROM sucursales_favoritas WHERE usuario_id=$1`, id); err != nil {
		return time.Time{}, err
	}
	// Account deletion revokes device destinations and cascades queued pushes.
	if _, err = tx.Exec(ctx, `DELETE FROM push_tokens WHERE usuario_id=$1`, id); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM web_push_subscriptions WHERE usuario_id=$1`, id); err != nil {
		return time.Time{}, err
	}
	// Remove the customer's ledger, but preserve other customers' balances and
	// movements after removing attribution to a departing staff member.
	for _, statement := range []string{
		`INSERT INTO profile_media_deletions(object_key,requested_at) SELECT substring(foto_url from 14),$2 FROM usuarios WHERE id=$1 AND foto_url LIKE 's3://puntazo/profiles/%' ON CONFLICT DO NOTHING`,
		`INSERT INTO account_deletion_journal(user_id,deleted_at) VALUES($1,$2) ON CONFLICT(user_id) DO NOTHING`,
		`UPDATE solicitudes_idempotentes SET response_body='{}'::bytea WHERE estado='COMPLETED' AND EXISTS(SELECT 1 FROM historial_movimientos h JOIN tarjetas t ON t.id=h.tarjeta_id WHERE t.usuario_id=$1 AND strpos(convert_from(response_body,'UTF8'),h.operation_id::text)>0) AND $2::timestamptz IS NOT NULL`,
		`UPDATE invitaciones_resenas SET tarjeta_id=NULL,operation_id=NULL,usuario_id=NULL,reservation_token=NULL,lease_until=NULL,cancelled_at=COALESCE(cancelled_at,$2) WHERE tarjeta_id IN(SELECT id FROM tarjetas WHERE usuario_id=$1) OR operation_id IN(SELECT operation_id FROM historial_movimientos WHERE tarjeta_id IN(SELECT id FROM tarjetas WHERE usuario_id=$1))`,
		`DELETE FROM previews_movimiento WHERE (cliente_id=$1 OR actor_id=$1) AND $2::timestamptz IS NOT NULL`,
		`DELETE FROM historial_movimientos WHERE tarjeta_id IN(SELECT id FROM tarjetas WHERE usuario_id=$1) AND $2::timestamptz IS NOT NULL`,
		`UPDATE historial_movimientos SET usuario_operador_id=NULL WHERE usuario_operador_id=$1 AND $2::timestamptz IS NOT NULL`,
		`DELETE FROM tarjetas WHERE usuario_id=$1 AND $2::timestamptz IS NOT NULL`,
	} {
		if _, err = tx.Exec(ctx, statement, id, deletedAt); err != nil {
			return time.Time{}, err
		}
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

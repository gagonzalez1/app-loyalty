package repository

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"time"

	"clientesFrecuentes/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrIdentityTokenInvalid = errors.New("identity token invalid")

func encryptOutboxToken(token, outboxID string, key []byte) ([]byte, []byte, error) {
	if token == "" || len(key) != 32 {
		return nil, nil, errors.New("secure outbox encryption key is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, []byte(token), []byte(outboxID)), nonce, nil
}

func DecryptOutboxToken(item model.OutboxEmail, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("secure outbox encryption key is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, item.Nonce, item.Ciphertext, []byte(item.ID))
	if err != nil {
		return "", errors.New("secure outbox payload is invalid")
	}
	return string(plaintext), nil
}

// ValidateClaimedEmail closes the gap between claiming and delivering an
// invitation: rotations, acceptance, revocation and expiry invalidate the
// encrypted payload before it can leave the process.
func (r *Repository) ValidateClaimedEmail(ctx context.Context, item model.OutboxEmail, token string) error {
	if item.Kind != "BRAND_INVITATION" {
		return nil
	}
	hash := sha256.Sum256([]byte(token))
	var valid bool
	err := r.Pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM email_outbox e JOIN invitaciones_marca i ON i.id=e.invitation_id
		WHERE e.id=$1 AND e.estado='SENDING' AND e.lease_owner=$2
		AND i.estado='PENDIENTE' AND i.expires_at>now() AND i.token_hash=$3
	)`, item.ID, item.LeaseOwner, hash[:]).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrInvitationInvalid
	}
	return nil
}

func enqueueIdentityEmail(ctx context.Context, tx pgx.Tx, key []byte, userID int64, tokenHash []byte, expiresAt time.Time, message model.EmailMessage) error {
	if _, err := tx.Exec(ctx, `UPDATE email_outbox SET estado='FAILED',ultimo_error='superseded',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL WHERE usuario_id=$1 AND tipo=$2 AND estado='PENDING'`, userID, message.Kind); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE tokens_identidad_email SET consumed_at=now() WHERE usuario_id=$1 AND proposito=$2 AND consumed_at IS NULL`, userID, message.Kind); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tokens_identidad_email(id,usuario_id,proposito,token_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, uuid.New(), userID, message.Kind, tokenHash, expiresAt); err != nil {
		return err
	}
	outboxID := uuid.New()
	ciphertext, nonce, err := encryptOutboxToken(message.Token, outboxID.String(), key)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO email_outbox(id,usuario_id,tipo,destinatario,asunto,token_ciphertext,token_nonce,token_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, outboxID, userID, message.Kind, message.To, message.Subject, ciphertext, nonce, expiresAt)
	return err
}

func (r *Repository) EnqueueVerification(ctx context.Context, email string, tokenHash []byte, expiresAt time.Time, message model.EmailMessage) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID int64
	err = tx.QueryRow(ctx, `SELECT id FROM usuarios WHERE email=$1 AND activo AND deleted_at IS NULL AND email_verified_at IS NULL FOR UPDATE`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = enqueueIdentityEmail(ctx, tx, r.OutboxCipherKey, userID, tokenHash, expiresAt, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) EnqueuePasswordReset(ctx context.Context, email string, tokenHash []byte, expiresAt time.Time, message model.EmailMessage) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID int64
	err = tx.QueryRow(ctx, `SELECT id FROM usuarios WHERE email=$1 AND activo AND deleted_at IS NULL AND email_verified_at IS NOT NULL AND password_hash IS NOT NULL FOR UPDATE`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = enqueueIdentityEmail(ctx, tx, r.OutboxCipherKey, userID, tokenHash, expiresAt, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) VerifyEmail(ctx context.Context, tokenHash []byte, now time.Time) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tokenID uuid.UUID
	var userID int64
	err = tx.QueryRow(ctx, `SELECT id,usuario_id FROM tokens_identidad_email WHERE token_hash=$1 AND proposito='VERIFY_EMAIL' AND consumed_at IS NULL AND expires_at>$2 FOR UPDATE`, tokenHash, now).Scan(&tokenID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentityTokenInvalid
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE usuarios SET email_verified_at=COALESCE(email_verified_at,$2) WHERE id=$1 AND activo AND deleted_at IS NULL`, userID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrIdentityTokenInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE tokens_identidad_email SET consumed_at=$2 WHERE id=$1`, tokenID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) ResetPassword(ctx context.Context, tokenHash []byte, passwordHash string, now time.Time) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tokenID uuid.UUID
	var userID int64
	err = tx.QueryRow(ctx, `SELECT id,usuario_id FROM tokens_identidad_email WHERE token_hash=$1 AND proposito='RESET_PASSWORD' AND consumed_at IS NULL AND expires_at>$2 FOR UPDATE`, tokenHash, now).Scan(&tokenID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentityTokenInvalid
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE usuarios SET password_hash=$2,auth_version=auth_version+1 WHERE id=$1 AND activo AND deleted_at IS NULL`, userID, passwordHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrIdentityTokenInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE tokens_identidad_email SET consumed_at=$2 WHERE usuario_id=$1 AND proposito='RESET_PASSWORD' AND consumed_at IS NULL`, userID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sesiones_auth SET revoked_at=COALESCE(revoked_at,$2) WHERE usuario_id=$1`, userID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) ClaimEmails(ctx context.Context, limit int) ([]model.OutboxEmail, error) {
	if _, err := r.Pool.Exec(ctx, `UPDATE email_outbox SET estado='FAILED',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL,ultimo_error='identity token expired' WHERE estado IN ('PENDING','SENDING') AND token_expires_at<=now()`); err != nil {
		return nil, err
	}
	leaseOwner := uuid.NewString()
	rows, err := r.Pool.Query(ctx, `WITH candidates AS (
		SELECT id FROM email_outbox
		WHERE token_expires_at>now() AND ((estado='PENDING' AND disponible_at<=now()) OR (estado='SENDING' AND lease_until<now()))
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1
	), claimed AS (
		UPDATE email_outbox e SET estado='SENDING',intentos=e.intentos+1,lease_until=now()+interval '2 minutes',lease_owner=$2
		FROM candidates c WHERE e.id=c.id
		RETURNING e.id::text,e.destinatario::text,e.tipo,e.token_ciphertext,e.token_nonce,e.token_expires_at,e.intentos,e.lease_owner::text,e.invitation_id
	)
	SELECT c.id,c.destinatario,c.tipo,c.token_ciphertext,c.token_nonce,c.token_expires_at,c.intentos,c.lease_owner,
		COALESCE(m.nombre,''),COALESCE(i.rol,''),
		COALESCE((SELECT array_agg(s.nombre::text ORDER BY s.nombre) FROM invitaciones_sucursales ins JOIN sucursales s ON s.id=ins.sucursal_id WHERE ins.invitacion_id=c.invitation_id),'{}'::text[]),
		COALESCE((SELECT a.object_key FROM archivos_marca a WHERE a.marca_id=i.marca_id AND a.tipo='LOGO' AND a.estado='ACTIVA' ORDER BY a.created_at DESC LIMIT 1),'')
	FROM claimed c
	LEFT JOIN invitaciones_marca i ON i.id=c.invitation_id
	LEFT JOIN marcas m ON m.id=i.marca_id`, limit, leaseOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.OutboxEmail, 0)
	for rows.Next() {
		var item model.OutboxEmail
		if err = rows.Scan(&item.ID, &item.To, &item.Kind, &item.Ciphertext, &item.Nonce, &item.ExpiresAt, &item.Attempts, &item.LeaseOwner, &item.BrandName, &item.InvitationRole, &item.InvitationBranchNames, &item.BrandLogoObjectKey); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *Repository) MarkEmailSent(ctx context.Context, id, leaseOwner string) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE email_outbox SET estado='SENT',sent_at=now(),lease_until=NULL,lease_owner=NULL,ultimo_error=NULL,token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL WHERE id=$1 AND estado='SENDING' AND lease_owner=$2`, id, leaseOwner)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return err
}
func (r *Repository) MarkEmailFailed(ctx context.Context, id, leaseOwner string, attempts int, sendErr error) error {
	delay := time.Duration(1<<min(attempts, 8)) * time.Minute
	state := "PENDING"
	if attempts >= 5 {
		state = "FAILED"
	}
	message := sendErr.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	tag, err := r.Pool.Exec(ctx, `UPDATE email_outbox SET estado=$3,disponible_at=$4,lease_until=NULL,lease_owner=NULL,ultimo_error=$5,token_ciphertext=CASE WHEN $3='FAILED' THEN NULL ELSE token_ciphertext END,token_nonce=CASE WHEN $3='FAILED' THEN NULL ELSE token_nonce END,token_expires_at=CASE WHEN $3='FAILED' THEN NULL ELSE token_expires_at END WHERE id=$1 AND estado='SENDING' AND lease_owner=$2`, id, leaseOwner, state, r.Now().Add(delay), message)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return err
}

package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"clientesFrecuentes/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type GiftCardToken struct {
	ID, Token, Hint string
	Hash            [32]byte
}

func GiftCardHash(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func (r *Repository) CreateGiftCardBatch(ctx context.Context, actorID, brandID int64, req model.GiftCardCreateRequest, tokens []GiftCardToken, claimBase string) (model.GiftCardBatch, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.GiftCardBatch{}, err
	}
	defer tx.Rollback(ctx)
	var programID int64
	var programType, brandName, branchName string
	var profile *model.BranchPublicProfile
	err = tx.QueryRow(ctx, `SELECT p.id,p.tipo,m.nombre,s.nombre,s.perfil_publico FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id AND m.activo JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo JOIN sucursales s ON s.id=$3 AND s.marca_id=m.id AND s.activo LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id AND ms.sucursal_id=s.id AND ms.activo WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo AND (mm.rol IN ('PROPIETARIO','ADMINISTRADOR') OR ms.sucursal_id IS NOT NULL)`, actorID, brandID, req.BranchID).Scan(&programID, &programType, &brandName, &branchName, &profile)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.GiftCardBatch{}, ErrNotFound
	}
	if err != nil {
		return model.GiftCardBatch{}, err
	}
	socialLinks := make([]model.GiftCardSocialLink, 0, len(req.SocialNetworks))
	for _, network := range req.SocialNetworks {
		if profile == nil {
			return model.GiftCardBatch{}, ErrGiftCardSocialUnavailable
		}
		link := strings.TrimSpace(profile.Links[network])
		if link == "" {
			return model.GiftCardBatch{}, ErrGiftCardSocialUnavailable
		}
		socialLinks = append(socialLinks, model.GiftCardSocialLink{Network: network, URL: link})
	}
	id := uuid.New()
	now := r.Now().UTC()
	_, err = tx.Exec(ctx, `INSERT INTO gift_card_batches(id,marca_id,sucursal_id,programa_id,creado_por,titulo,programa_tipo,cantidad,total_codigos,expires_at,created_at,social_links) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb)`, id, brandID, req.BranchID, programID, actorID, strings.TrimSpace(req.Title), programType, req.Amount, req.Quantity, req.ExpiresAt, now, socialLinks)
	if err != nil {
		return model.GiftCardBatch{}, err
	}
	codes := make([]model.GiftCardCode, 0, len(tokens))
	for _, token := range tokens {
		ciphertext, nonce, encryptErr := encryptOutboxToken(token.Token, "gift-card:"+token.ID, r.OutboxCipherKey)
		if encryptErr != nil {
			return model.GiftCardBatch{}, encryptErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO gift_card_codes(id,batch_id,token_hash,token_hint,token_ciphertext,token_nonce) VALUES($1,$2,$3,$4,$5,$6)`, token.ID, id, token.Hash[:], token.Hint, ciphertext, nonce)
		if err != nil {
			return model.GiftCardBatch{}, err
		}
		codes = append(codes, model.GiftCardCode{ID: token.ID, Token: token.Token, ClaimURL: strings.TrimRight(claimBase, "/") + "/gift-card/" + token.Token, TokenHint: token.Hint})
	}
	if err = tx.Commit(ctx); err != nil {
		return model.GiftCardBatch{}, normalize(err)
	}
	return model.GiftCardBatch{ID: id.String(), BrandID: brandID, BrandName: brandName, BranchID: req.BranchID, BranchName: branchName, Title: strings.TrimSpace(req.Title), ProgramType: programType, Amount: req.Amount, Quantity: req.Quantity, Status: "ACTIVA", ExpiresAt: req.ExpiresAt, CreatedAt: now, Codes: codes, SocialLinks: socialLinks}, nil
}

func (r *Repository) GiftCardBatch(ctx context.Context, actorID, brandID int64, batchID, claimBase string) (model.GiftCardBatch, error) {
	var batch model.GiftCardBatch
	err := r.Pool.QueryRow(ctx, `SELECT b.id,b.marca_id,m.nombre,b.sucursal_id,s.nombre,b.titulo,b.programa_tipo,b.cantidad,b.total_codigos,count(c.redeemed_at),b.estado,b.expires_at,b.created_at,b.social_links
		FROM gift_card_batches b
		JOIN marcas m ON m.id=b.marca_id
		JOIN sucursales s ON s.id=b.sucursal_id
		JOIN membresias_marca mm ON mm.marca_id=b.marca_id AND mm.usuario_id=$1 AND mm.activo
		LEFT JOIN gift_card_codes c ON c.batch_id=b.id
		WHERE b.marca_id=$2 AND b.id=$3 AND b.deleted_at IS NULL
		GROUP BY b.id,m.nombre,s.nombre`, actorID, brandID, batchID).
		Scan(&batch.ID, &batch.BrandID, &batch.BrandName, &batch.BranchID, &batch.BranchName, &batch.Title, &batch.ProgramType, &batch.Amount, &batch.Quantity, &batch.RedeemedCount, &batch.Status, &batch.ExpiresAt, &batch.CreatedAt, &batch.SocialLinks)
	if errors.Is(err, pgx.ErrNoRows) {
		return batch, ErrNotFound
	}
	if err != nil {
		return batch, err
	}

	rows, err := r.Pool.Query(ctx, `SELECT gc.id,gc.token_hint,gc.redeemed_at,gc.token_ciphertext,gc.token_nonce,
		COALESCE(u.codigo_usuario,'#USER-'||lpad(u.id::text,greatest(4,length(u.id::text)),'0')),
		btrim(concat_ws(' ',u.nombre,u.apellido)),u.email::text
		FROM gift_card_codes gc
		LEFT JOIN usuarios u ON u.id=gc.redeemed_by
		WHERE gc.batch_id=$1
		ORDER BY gc.created_at,gc.id`, batchID)
	if err != nil {
		return batch, err
	}
	defer rows.Close()
	batch.Codes = make([]model.GiftCardCode, 0, batch.Quantity)
	printable := batch.Status == "ACTIVA" && (batch.ExpiresAt == nil || r.Now().UTC().Before(*batch.ExpiresAt))
	for rows.Next() {
		var code model.GiftCardCode
		var ciphertext, nonce []byte
		var userCode, name, email *string
		if err = rows.Scan(&code.ID, &code.TokenHint, &code.RedeemedAt, &ciphertext, &nonce, &userCode, &name, &email); err != nil {
			return batch, err
		}
		code.Redeemed = code.RedeemedAt != nil
		if code.Redeemed {
			if userCode != nil && name != nil && email != nil {
				code.Redeemer = &model.GiftCardRedeemer{UserCode: *userCode, Name: *name, Email: *email}
			}
		} else if printable && len(ciphertext) > 0 && len(nonce) > 0 {
			token, decryptErr := DecryptOutboxToken(model.OutboxEmail{ID: "gift-card:" + code.ID, Ciphertext: ciphertext, Nonce: nonce}, r.OutboxCipherKey)
			if decryptErr != nil {
				return batch, fmt.Errorf("decrypt gift card token: %w", decryptErr)
			}
			code.ClaimURL = strings.TrimRight(claimBase, "/") + "/gift-card/" + token
		}
		batch.Codes = append(batch.Codes, code)
	}
	return batch, rows.Err()
}

func (r *Repository) ListGiftCardBatches(ctx context.Context, actorID, brandID int64) ([]model.GiftCardBatch, error) {
	rows, err := r.Pool.Query(ctx, `SELECT b.id,b.marca_id,m.nombre,b.sucursal_id,s.nombre,b.titulo,b.programa_tipo,b.cantidad,b.total_codigos,count(c.redeemed_at),b.estado,b.expires_at,b.created_at FROM gift_card_batches b JOIN marcas m ON m.id=b.marca_id JOIN sucursales s ON s.id=b.sucursal_id JOIN membresias_marca mm ON mm.marca_id=b.marca_id AND mm.usuario_id=$1 AND mm.activo LEFT JOIN gift_card_codes c ON c.batch_id=b.id WHERE b.marca_id=$2 AND b.deleted_at IS NULL GROUP BY b.id,m.nombre,s.nombre ORDER BY b.created_at DESC`, actorID, brandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.GiftCardBatch{}
	for rows.Next() {
		var b model.GiftCardBatch
		if err = rows.Scan(&b.ID, &b.BrandID, &b.BrandName, &b.BranchID, &b.BranchName, &b.Title, &b.ProgramType, &b.Amount, &b.Quantity, &b.RedeemedCount, &b.Status, &b.ExpiresAt, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *Repository) RevokeGiftCardBatch(ctx context.Context, actorID, brandID int64, batchID string) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE gift_card_batches b SET estado='REVOCADA' FROM membresias_marca mm WHERE b.id=$1 AND b.marca_id=$2 AND b.deleted_at IS NULL AND mm.marca_id=b.marca_id AND mm.usuario_id=$3 AND mm.activo AND mm.rol IN ('PROPIETARIO','ADMINISTRADOR')`, batchID, brandID, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) DeleteGiftCardBatch(ctx context.Context, actorID, brandID int64, batchID string) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE gift_card_batches b SET estado='REVOCADA',deleted_at=now() FROM membresias_marca mm WHERE b.id=$1 AND b.marca_id=$2 AND b.deleted_at IS NULL AND mm.marca_id=b.marca_id AND mm.usuario_id=$3 AND mm.activo AND mm.rol IN ('PROPIETARIO','ADMINISTRADOR')`, batchID, brandID, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) GiftCardClaim(ctx context.Context, token string) (model.GiftCardClaim, error) {
	h := GiftCardHash(token)
	var c model.GiftCardClaim
	err := r.Pool.QueryRow(ctx, `SELECT m.nombre,s.nombre,b.titulo,b.programa_tipo,b.cantidad,b.expires_at,(gc.redeemed_at IS NOT NULL) FROM gift_card_codes gc JOIN gift_card_batches b ON b.id=gc.batch_id JOIN marcas m ON m.id=b.marca_id JOIN sucursales s ON s.id=b.sucursal_id WHERE gc.token_hash=$1 AND b.estado='ACTIVA' AND b.deleted_at IS NULL`, h[:]).Scan(&c.BrandName, &c.BranchName, &c.Title, &c.ProgramType, &c.Amount, &c.ExpiresAt, &c.Redeemed)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if c.ExpiresAt != nil && !r.Now().UTC().Before(*c.ExpiresAt) {
		return c, ErrNotFound
	}
	return c, nil
}

func (r *Repository) RedeemGiftCard(ctx context.Context, customerID int64, token string) (model.GiftCardRedemption, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.GiftCardRedemption{}, err
	}
	defer tx.Rollback(ctx)
	h := GiftCardHash(token)
	var codeID, batchID string
	var brandID, branchID, programID, amount int64
	var title, programType, brandName, branchName, status string
	var expires, redeemed *time.Time
	err = tx.QueryRow(ctx, `SELECT gc.id,gc.batch_id,b.marca_id,b.sucursal_id,b.programa_id,b.cantidad,b.titulo,b.programa_tipo,m.nombre,s.nombre,b.estado,b.expires_at,gc.redeemed_at FROM gift_card_codes gc JOIN gift_card_batches b ON b.id=gc.batch_id JOIN marcas m ON m.id=b.marca_id AND m.activo JOIN sucursales s ON s.id=b.sucursal_id AND s.activo JOIN programas_fidelidad p ON p.id=b.programa_id AND p.activo WHERE gc.token_hash=$1 AND b.deleted_at IS NULL FOR UPDATE OF gc,b`, h[:]).Scan(&codeID, &batchID, &brandID, &branchID, &programID, &amount, &title, &programType, &brandName, &branchName, &status, &expires, &redeemed)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.GiftCardRedemption{}, ErrNotFound
	}
	if err != nil {
		return model.GiftCardRedemption{}, err
	}
	if status != "ACTIVA" || redeemed != nil || (expires != nil && !r.Now().UTC().Before(*expires)) {
		return model.GiftCardRedemption{}, ErrGiftCardUnavailable
	}
	var accountType string
	if err = tx.QueryRow(ctx, `SELECT tipo_cuenta FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL FOR SHARE`, customerID).Scan(&accountType); err != nil {
		return model.GiftCardRedemption{}, ErrNotFound
	}
	if accountType != "CLIENTE_FINAL" {
		return model.GiftCardRedemption{}, ErrInvalidRequest
	}
	_, err = tx.Exec(ctx, `INSERT INTO tarjetas(usuario_id,marca_id) VALUES($1,$2) ON CONFLICT(usuario_id,marca_id) DO NOTHING`, customerID, brandID)
	if err != nil {
		return model.GiftCardRedemption{}, err
	}
	var cardID, before int64
	err = tx.QueryRow(ctx, `SELECT id,CASE WHEN $3='PUNTOS' THEN saldo_puntos ELSE saldo_sellos END FROM tarjetas WHERE usuario_id=$1 AND marca_id=$2 AND activo FOR UPDATE`, customerID, brandID, programType).Scan(&cardID, &before)
	if err != nil {
		return model.GiftCardRedemption{}, err
	}
	after := before + amount
	column := "saldo_sellos"
	if programType == "PUNTOS" {
		column = "saldo_puntos"
	}
	if _, err = tx.Exec(ctx, `UPDATE tarjetas SET `+column+`=$1,version=version+1 WHERE id=$2`, after, cardID); err != nil {
		return model.GiftCardRedemption{}, err
	}
	opID := uuid.New()
	var movement model.Movement
	err = tx.QueryRow(ctx, `INSERT INTO historial_movimientos(operation_id,tarjeta_id,marca_id,sucursal_id,usuario_operador_id,operacion,sentido,cantidad,saldo_anterior,saldo_posterior,programa_tipo,marca_nombre_snapshot,sucursal_nombre_snapshot,programa_id_snapshot) VALUES($1,$2,$3,$4,NULL,'ACUMULACION','CREDITO',$5,$6,$7,$8,$9,$10,$11) RETURNING id,occurred_at`, opID, cardID, brandID, branchID, amount, before, after, programType, brandName, branchName, programID).Scan(&movement.ID, &movement.OccurredAt)
	if err != nil {
		return model.GiftCardRedemption{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE gift_card_codes SET redeemed_by=$1,movement_id=$2,redeemed_at=now() WHERE id=$3 AND redeemed_at IS NULL`, customerID, movement.ID, codeID)
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return model.GiftCardRedemption{}, err
		}
		return model.GiftCardRedemption{}, ErrGiftCardUnavailable
	}
	movement.OperationID = opID.String()
	movement.CardID = cardID
	movement.BrandID = brandID
	movement.BrandName = brandName
	movement.BranchID = branchID
	movement.BranchName = branchName
	movement.Operation = "ACUMULACION"
	movement.ProgramType = programType
	movement.ProgramIDSnapshot = programID
	movement.Direction = "CREDITO"
	movement.Amount = amount
	movement.BalanceBefore = before
	movement.BalanceAfter = after
	if _, err = tx.Exec(ctx, `SELECT pg_notify('puntazo_customer_cards',$1::bigint::text)`, customerID); err != nil {
		return model.GiftCardRedemption{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.GiftCardRedemption{}, normalize(err)
	}
	return model.GiftCardRedemption{Claim: model.GiftCardClaim{BrandName: brandName, BranchName: branchName, Title: title, ProgramType: programType, Amount: amount, ExpiresAt: expires, Redeemed: true}, Movement: movement}, nil
}

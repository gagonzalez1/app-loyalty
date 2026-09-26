package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"clientesFrecuentes/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreatePreview(ctx context.Context, actorID int64, req model.MovementPreviewRequest, qrHash, fingerprint []byte) (model.Preview, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return model.Preview{}, err
	}
	defer tx.Rollback(ctx)
	var brandID, programID int64
	var branchName, programType string
	err = tx.QueryRow(ctx, `SELECT s.marca_id,s.nombre,p.id,p.tipo FROM membresias_marca mm JOIN sucursales s ON s.marca_id=mm.marca_id AND s.activo LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id AND ms.marca_id=mm.marca_id AND ms.sucursal_id=s.id AND ms.activo JOIN marcas m ON m.id=s.marca_id AND m.activo JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo JOIN accesos_demo a ON a.marca_id=m.id AND a.activo WHERE mm.usuario_id=$1 AND mm.activo AND s.id=$2 AND (mm.rol IN ('PROPIETARIO','ADMINISTRADOR') OR ms.sucursal_id IS NOT NULL) AND EXISTS(SELECT 1 FROM beneficios configured WHERE configured.programa_id=p.id AND configured.activo)`, actorID, req.BranchID).Scan(&brandID, &branchName, &programID, &programType)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Preview{}, ErrNotFound
	}
	if err != nil {
		return model.Preview{}, err
	}
	var customerID int64
	var customerName string
	err = tx.QueryRow(ctx, `SELECT id,nombre FROM usuarios WHERE qr_hash=$1 AND tipo_cuenta='CLIENTE_FINAL' AND activo AND deleted_at IS NULL`, qrHash).Scan(&customerID, &customerName)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Preview{}, ErrNotFound
	}
	if err != nil {
		return model.Preview{}, err
	}
	var cardID *int64
	balance := int64(0)
	err = tx.QueryRow(ctx, `SELECT id,CASE WHEN $3='PUNTOS' THEN saldo_puntos ELSE saldo_sellos END FROM tarjetas WHERE usuario_id=$1 AND marca_id=$2 AND activo`, customerID, brandID, programType).Scan(&cardID, &balance)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return model.Preview{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		cardID = nil
		balance = 0
	}
	amount := int64(1)
	if programType == "PUNTOS" {
		if req.PointsAmount == nil || *req.PointsAmount < 1 || *req.PointsAmount > 100000 {
			return model.Preview{}, ErrInvalidRequest
		}
		amount = *req.PointsAmount
	} else if req.PointsAmount != nil {
		return model.Preview{}, ErrInvalidRequest
	}
	after := balance + amount
	var benefit *model.Benefit
	if req.Operation == "CANJE" {
		if req.BenefitID == nil {
			return model.Preview{}, ErrPreviewChanged
		}
		var b model.Benefit
		err = tx.QueryRow(ctx, `SELECT id,programa_id,nombre,requisito_sellos,requisito_puntos,activo,version FROM beneficios WHERE id=$1 AND programa_id=$2 AND activo FOR SHARE`, *req.BenefitID, programID).Scan(&b.ID, &b.ProgramID, &b.Name, &b.RequiredStamps, &b.RequiredPoints, &b.Active, &b.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Preview{}, ErrNotFound
		}
		if err != nil {
			return model.Preview{}, err
		}
		if programType == "PUNTOS" {
			amount = *b.RequiredPoints
		} else {
			amount = *b.RequiredStamps
		}
		if balance < amount {
			return model.Preview{}, ErrInsufficientBalance
		}
		after = balance - amount
		benefit = &b
	} else if req.Operation != "ACUMULACION" {
		return model.Preview{}, ErrPreviewChanged
	}
	id := uuid.New()
	expires := r.Now().UTC().Add(5 * time.Minute)
	_, err = tx.Exec(ctx, `INSERT INTO previews_movimiento(id,actor_id,sucursal_id,marca_id,cliente_id,tarjeta_id,operacion,beneficio_id,qr_hash,saldo_anterior,cantidad,saldo_posterior,request_fingerprint,expires_at,programa_tipo) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, actorID, req.BranchID, brandID, customerID, cardID, req.Operation, req.BenefitID, qrHash, balance, amount, after, fingerprint, expires, programType)
	if err != nil {
		return model.Preview{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Preview{}, err
	}
	cardValue := int64(0)
	if cardID != nil {
		cardValue = *cardID
	}
	return model.Preview{ID: id.String(), ExpiresAt: expires, Operation: req.Operation, ProgramType: programType, Customer: model.PreviewCustomer{ID: customerID, Name: customerName}, CardID: cardValue, BalanceBefore: balance, Amount: amount, BalanceAfter: after, Benefit: benefit}, nil
}

type ConfirmInput struct {
	ActorID     int64
	Key         string
	Fingerprint []byte
	PreviewID   string
	QRHash      []byte
	BranchID    int64
	BenefitID   *int64
	Operation   string
}

func (r *Repository) ConfirmMovement(ctx context.Context, in ConfirmInput, build func(model.Movement) ([]byte, error)) (IdempotentResult, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IdempotentResult{}, err
	}
	defer tx.Rollback(ctx)
	operationName := "CONFIRM_" + in.Operation
	claimed, err := claimIdempotency(ctx, tx, in.Key, fmt.Sprintf("user:%d", in.ActorID), operationName, in.Fingerprint)
	if err != nil {
		return IdempotentResult{}, err
	}
	if claimed != nil {
		return *claimed, nil
	}
	type previewRow struct {
		ActorID, BranchID, BrandID, CustomerID int64
		CardID                                 *int64
		Operation, ProgramType                 string
		BenefitID                              *int64
		QRHash                                 []byte
		Before, Amount, After                  int64
		Expires                                time.Time
		Consumed                               *time.Time
	}
	var p previewRow
	err = tx.QueryRow(ctx, `SELECT actor_id,sucursal_id,marca_id,cliente_id,tarjeta_id,operacion,programa_tipo,beneficio_id,qr_hash,saldo_anterior,cantidad,saldo_posterior,expires_at,consumed_at FROM previews_movimiento WHERE id=$1 FOR UPDATE`, in.PreviewID).
		Scan(&p.ActorID, &p.BranchID, &p.BrandID, &p.CustomerID, &p.CardID, &p.Operation, &p.ProgramType, &p.BenefitID, &p.QRHash, &p.Before, &p.Amount, &p.After, &p.Expires, &p.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotentResult{}, ErrNotFound
	}
	if err != nil {
		return IdempotentResult{}, err
	}
	if p.ActorID != in.ActorID || p.BranchID != in.BranchID || p.Operation != in.Operation || !bytes.Equal(p.QRHash, in.QRHash) || !sameOptionalID(p.BenefitID, in.BenefitID) {
		return IdempotentResult{}, ErrPreviewChanged
	}
	if p.Consumed != nil {
		return IdempotentResult{}, ErrPreviewConsumed
	}
	if !r.Now().UTC().Before(p.Expires) {
		return IdempotentResult{}, ErrPreviewExpired
	}
	var branchName, brandName, programType string
	var programID int64
	err = tx.QueryRow(ctx, `SELECT s.nombre,m.nombre,p.id,p.tipo FROM membresias_marca mm JOIN sucursales s ON s.marca_id=mm.marca_id AND s.activo LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id AND ms.marca_id=mm.marca_id AND ms.sucursal_id=s.id AND ms.activo JOIN marcas m ON m.id=s.marca_id AND m.activo JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo JOIN accesos_demo a ON a.marca_id=m.id AND a.activo WHERE mm.usuario_id=$1 AND mm.activo AND s.id=$2 AND m.id=$3 AND (mm.rol IN ('PROPIETARIO','ADMINISTRADOR') OR ms.sucursal_id IS NOT NULL) FOR SHARE OF p`, in.ActorID, in.BranchID, p.BrandID).Scan(&branchName, &brandName, &programID, &programType)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotentResult{}, ErrNotFound
	}
	if err != nil {
		return IdempotentResult{}, err
	}
	if programType != p.ProgramType {
		return IdempotentResult{}, ErrPreviewChanged
	}
	var customerID int64
	if err = tx.QueryRow(ctx, `SELECT id FROM usuarios WHERE qr_hash=$1 AND activo AND deleted_at IS NULL AND tipo_cuenta='CLIENTE_FINAL' FOR SHARE`, in.QRHash).Scan(&customerID); errors.Is(err, pgx.ErrNoRows) {
		return IdempotentResult{}, ErrNotFound
	}
	if err != nil {
		return IdempotentResult{}, err
	}
	if customerID != p.CustomerID {
		return IdempotentResult{}, ErrPreviewChanged
	}
	var benefitName *string
	var benefitRequirement *int64
	if in.Operation == "ACUMULACION" {
		_, err = tx.Exec(ctx, `INSERT INTO tarjetas(usuario_id,marca_id) VALUES($1,$2) ON CONFLICT(usuario_id,marca_id) DO NOTHING`, p.CustomerID, p.BrandID)
		if err != nil {
			return IdempotentResult{}, err
		}
	}
	var cardID, balance int64
	err = tx.QueryRow(ctx, `SELECT id,CASE WHEN $3='PUNTOS' THEN saldo_puntos ELSE saldo_sellos END FROM tarjetas WHERE usuario_id=$1 AND marca_id=$2 AND activo FOR UPDATE`, p.CustomerID, p.BrandID, programType).Scan(&cardID, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotentResult{}, ErrInsufficientBalance
	}
	if err != nil {
		return IdempotentResult{}, err
	}
	// The accepted global lock order is card, then benefit. Demo access is
	// revalidated above but is not locked because it is immutable in this slice.
	if in.Operation == "CANJE" {
		var n string
		var required int64
		err = tx.QueryRow(ctx, `SELECT nombre,CASE WHEN $3='PUNTOS' THEN requisito_puntos ELSE requisito_sellos END FROM beneficios WHERE id=$1 AND programa_id=$2 AND activo FOR SHARE`, *in.BenefitID, programID, programType).Scan(&n, &required)
		if errors.Is(err, pgx.ErrNoRows) {
			return IdempotentResult{}, ErrNotFound
		}
		if err != nil {
			return IdempotentResult{}, err
		}
		if required != p.Amount {
			return IdempotentResult{}, ErrPreviewChanged
		}
		benefitName = &n
		benefitRequirement = &required
	}
	if balance != p.Before {
		return IdempotentResult{}, ErrPreviewChanged
	}
	after := balance + p.Amount
	direction := "CREDITO"
	if in.Operation == "CANJE" {
		if balance < p.Amount {
			return IdempotentResult{}, ErrInsufficientBalance
		}
		after = balance - p.Amount
		direction = "DEBITO"
	}
	balanceColumn := "saldo_sellos"
	if programType == "PUNTOS" {
		balanceColumn = "saldo_puntos"
	}
	if _, err = tx.Exec(ctx, `UPDATE tarjetas SET `+balanceColumn+`=$1,version=version+1 WHERE id=$2`, after, cardID); err != nil {
		return IdempotentResult{}, err
	}
	operationID := uuid.New()
	var m model.Movement
	var requiredStamps, requiredPoints *int64
	if programType == "PUNTOS" {
		requiredPoints = benefitRequirement
	} else {
		requiredStamps = benefitRequirement
	}
	err = tx.QueryRow(ctx, `INSERT INTO historial_movimientos(operation_id,tarjeta_id,marca_id,sucursal_id,usuario_operador_id,beneficio_id,operacion,sentido,cantidad,saldo_anterior,saldo_posterior,beneficio_nombre_snapshot,beneficio_requisito_snapshot,programa_tipo,beneficio_requisito_puntos_snapshot,marca_nombre_snapshot,sucursal_nombre_snapshot,programa_id_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id,occurred_at`, operationID, cardID, p.BrandID, in.BranchID, in.ActorID, in.BenefitID, in.Operation, direction, p.Amount, balance, after, benefitName, requiredStamps, programType, requiredPoints, brandName, branchName, programID).Scan(&m.ID, &m.OccurredAt)
	if err != nil {
		return IdempotentResult{}, err
	}
	m.OperationID = operationID.String()
	m.CardID = cardID
	m.BrandID = p.BrandID
	m.BrandName = brandName
	m.BranchID = in.BranchID
	m.BranchName = branchName
	m.Operation = in.Operation
	m.ProgramType = programType
	m.ProgramIDSnapshot = programID
	m.Direction = direction
	m.Amount = p.Amount
	m.BalanceBefore = balance
	m.BalanceAfter = after
	m.BenefitNameSnapshot = benefitName
	m.BenefitRequiredStampsSnapshot = requiredStamps
	m.BenefitRequiredPointsSnapshot = requiredPoints
	if _, err = tx.Exec(ctx, `UPDATE previews_movimiento SET consumed_at=now() WHERE id=$1`, in.PreviewID); err != nil {
		return IdempotentResult{}, err
	}
	body, err := build(m)
	if err != nil {
		return IdempotentResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE solicitudes_idempotentes SET estado='COMPLETED',response_status=201,response_body=$2,completed_at=now() WHERE idempotency_key=$1`, in.Key, []byte(body)); err != nil {
		return IdempotentResult{}, err
	}
	// PostgreSQL delivers a NOTIFY only if this transaction commits. Replayed
	// idempotency requests return above and cannot emit a second card event.
	if _, err = tx.Exec(ctx, `SELECT pg_notify('puntazo_customer_cards',$1)`, strconv.FormatInt(p.CustomerID, 10)); err != nil {
		return IdempotentResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IdempotentResult{}, normalize(err)
	}
	return IdempotentResult{Status: 201, Body: body}, nil
}

func sameOptionalID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (r *Repository) GetMovementIdempotency(ctx context.Context, actorID int64, key string) (json.RawMessage, error) {
	var state string
	var body []byte
	err := r.Pool.QueryRow(ctx, `SELECT estado,response_body FROM solicitudes_idempotentes WHERE idempotency_key=$1 AND actor_scope=$2 AND operacion IN ('CONFIRM_ACUMULACION','CONFIRM_CANJE')`, key, fmt.Sprintf("user:%d", actorID)).Scan(&state, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if state != "COMPLETED" {
		return nil, ErrIdempotencyInProgress
	}
	return json.RawMessage(body), nil
}

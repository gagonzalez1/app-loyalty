package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const MaxSubscriptionAmountMinor int64 = 9007199254740991 // Exact integer in JSON clients.

type SubscriptionPrice struct {
	ProgramType    string     `json:"program_type"`
	UnitPriceMinor int64      `json:"unit_price_minor"`
	Version        int64      `json:"version"`
	Customized     bool       `json:"customized"`
	UpdatedAt      *time.Time `json:"updated_at"`
}
type SubscriptionPricePreview struct {
	BrandID                  int64  `json:"brand_id"`
	BrandName                string `json:"brand_name"`
	Status                   string `json:"subscription_status"`
	Branches                 int64  `json:"branches"`
	CurrentAmountMinor       int64  `json:"current_amount_minor"`
	NewAmountMinor           int64  `json:"new_amount_minor"`
	DiscountBPS              int    `json:"discount_bps"`
	DiscountRemainingCharges int    `json:"discount_remaining_charges"`
}
type SubscriptionPriceChangeItem struct {
	BrandID     int64  `json:"brand_id"`
	BrandName   string `json:"brand_name"`
	Status      string `json:"status"`
	AmountMinor *int64 `json:"amount_minor"`
}
type SubscriptionPriceChange struct {
	ID                     string                        `json:"id"`
	ProgramType            string                        `json:"program_type"`
	UnitPriceMinor         int64                         `json:"unit_price_minor"`
	PreviousUnitPriceMinor *int64                        `json:"previous_unit_price_minor"`
	AdminEmail             string                        `json:"admin_email"`
	PreviousVersion        int64                         `json:"previous_version"`
	PriceVersion           int64                         `json:"price_version"`
	IncludeExisting        bool                          `json:"include_existing"`
	CreatedAt              time.Time                     `json:"created_at"`
	Items                  []SubscriptionPriceChangeItem `json:"items"`
}

func validSubscriptionPrice(program string, amount int64) bool {
	return (program == "SELLOS" || program == "PUNTOS") && amount > 0 && amount <= MaxSubscriptionAmountMinor
}

func (r *Repository) SubscriptionPrice(ctx context.Context, program string, fallback int64) (SubscriptionPrice, error) {
	x := SubscriptionPrice{ProgramType: program, UnitPriceMinor: fallback}
	if !validSubscriptionPrice(program, fallback) {
		return x, ErrInvalidRequest
	}
	err := r.Pool.QueryRow(ctx, `SELECT unit_price_minor,version,updated_at FROM subscription_prices WHERE program_type=$1`, program).Scan(&x.UnitPriceMinor, &x.Version, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, nil
	}
	x.Customized = err == nil
	return x, err
}

func (r *Repository) PreviewSubscriptionPrice(ctx context.Context, program string, amount int64) ([]SubscriptionPricePreview, error) {
	if !validSubscriptionPrice(program, amount) {
		return nil, ErrInvalidRequest
	}
	rows, err := r.Pool.Query(ctx, `SELECT s.marca_id,m.nombre,s.estado,s.cantidad_sucursales,s.importe_mensual_minor,
 COALESCE(a.discount_bps,0),greatest(COALESCE(a.discount_charges,0)-(SELECT count(*) FROM referral_charges c WHERE c.brand_id=s.marca_id),0)
 FROM suscripciones_marca s JOIN marcas m ON m.id=s.marca_id AND m.activo AND m.deleted_at IS NULL
 LEFT JOIN referral_attributions a ON a.brand_id=s.marca_id
 LEFT JOIN LATERAL (SELECT tipo FROM programas_fidelidad WHERE marca_id=s.marca_id ORDER BY activo DESC,created_at DESC,id DESC LIMIT 1) p ON true
 WHERE COALESCE(s.program_type,p.tipo)=$1 AND s.estado IN ('PENDING','AUTHORIZED','PAUSED')
 ORDER BY m.nombre,s.marca_id`, program)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SubscriptionPricePreview, 0)
	for rows.Next() {
		var x SubscriptionPricePreview
		if err := rows.Scan(&x.BrandID, &x.BrandName, &x.Status, &x.Branches, &x.CurrentAmountMinor, &x.DiscountBPS, &x.DiscountRemainingCharges); err != nil {
			return nil, err
		}
		if x.Branches < 1 || amount > MaxSubscriptionAmountMinor/x.Branches {
			return nil, ErrInvalidRequest
		}
		unit := amount
		if x.DiscountRemainingCharges > 0 {
			unit = discountedMinor(unit, x.DiscountBPS)
		}
		if unit < 1 {
			return nil, ErrInvalidRequest
		}
		x.NewAmountMinor = unit * x.Branches
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r *Repository) ChangeSubscriptionPrice(ctx context.Context, adminID int64, key uuid.UUID, program string, amount, expectedVersion, fallback int64, includeExisting bool) (SubscriptionPriceChange, error) {
	var zero SubscriptionPriceChange
	if !validSubscriptionPrice(program, amount) || !validSubscriptionPrice(program, fallback) || expectedVersion < 0 {
		return zero, ErrInvalidRequest
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	// Serialize price updates and idempotent retries, including first-time settings.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(42024,hashtext($1))`, program); err != nil {
		return zero, err
	}
	var old SubscriptionPriceChange
	err = tx.QueryRow(ctx, `SELECT program_type,unit_price_minor,previous_version,include_existing FROM subscription_price_changes WHERE id=$1`, key).Scan(&old.ProgramType, &old.UnitPriceMinor, &old.PreviousVersion, &old.IncludeExisting)
	if err == nil {
		if old.ProgramType != program || old.UnitPriceMinor != amount || old.PreviousVersion != expectedVersion || old.IncludeExisting != includeExisting {
			return zero, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return zero, err
		}
		return r.SubscriptionPriceChange(ctx, key)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	var version int64
	var previousAmount *int64
	err = tx.QueryRow(ctx, `SELECT version,unit_price_minor FROM subscription_prices WHERE program_type=$1 FOR UPDATE`, program).Scan(&version, &previousAmount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if version != expectedVersion {
		return zero, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO subscription_prices(program_type,unit_price_minor,version,updated_by) VALUES($1,$2,1,$3)
 ON CONFLICT(program_type) DO UPDATE SET unit_price_minor=EXCLUDED.unit_price_minor,version=subscription_prices.version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, program, amount, adminID)
	if err != nil {
		return zero, err
	}
	previousPrice := fallback
	if previousAmount != nil {
		previousPrice = *previousAmount
	}
	if _, err = tx.Exec(ctx, `INSERT INTO subscription_price_changes(id,program_type,unit_price_minor,previous_version,price_version,include_existing,admin_id,previous_unit_price_minor) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, key, program, amount, version, version+1, includeExisting, adminID, previousPrice); err != nil {
		if IsUniqueViolation(err) {
			return zero, ErrConflict
		}
		return zero, err
	}
	if includeExisting {
		// Preview validates totals; this transaction fixes the actual affected cohort.
		var invalid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM suscripciones_marca s JOIN marcas m ON m.id=s.marca_id AND m.activo AND m.deleted_at IS NULL
 LEFT JOIN LATERAL(SELECT tipo FROM programas_fidelidad WHERE marca_id=s.marca_id ORDER BY activo DESC,created_at DESC,id DESC LIMIT 1) p ON true
 WHERE COALESCE(s.program_type,p.tipo)=$1 AND s.estado IN ('PENDING','AUTHORIZED','PAUSED') AND s.cantidad_sucursales>$3::bigint/$2::bigint)`, program, amount, MaxSubscriptionAmountMinor).Scan(&invalid); err != nil {
			return zero, err
		}
		if invalid {
			return zero, ErrInvalidRequest
		}
		if _, err = tx.Exec(ctx, `INSERT INTO subscription_price_change_items(change_id,brand_id,provider_subscription_id,external_reference)
 SELECT $1,s.marca_id,s.proveedor_suscripcion_id,s.referencia_externa FROM suscripciones_marca s
 JOIN marcas m ON m.id=s.marca_id AND m.activo AND m.deleted_at IS NULL
 LEFT JOIN LATERAL(SELECT tipo FROM programas_fidelidad WHERE marca_id=s.marca_id ORDER BY activo DESC,created_at DESC,id DESC LIMIT 1) p ON true
 WHERE COALESCE(s.program_type,p.tipo)=$2 AND s.estado IN ('PENDING','AUTHORIZED','PAUSED')`, key, program); err != nil {
			return zero, err
		}
	}
	if err = auditReferral(ctx, tx, adminID, "subscription_price.change", "program", program, map[string]any{"change_id": key, "previous_customized_unit_price_minor": previousAmount, "previous_unit_price_minor": previousPrice, "unit_price_minor": amount, "version": version + 1, "include_existing": includeExisting}); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return r.SubscriptionPriceChange(ctx, key)
}

func (r *Repository) SubscriptionPriceChange(ctx context.Context, key uuid.UUID) (SubscriptionPriceChange, error) {
	x := SubscriptionPriceChange{ID: key.String(), Items: make([]SubscriptionPriceChangeItem, 0)}
	err := r.Pool.QueryRow(ctx, `SELECT j.program_type,j.unit_price_minor,j.previous_version,j.price_version,j.include_existing,j.created_at,j.previous_unit_price_minor,u.email FROM subscription_price_changes j JOIN backoffice_users u ON u.id=j.admin_id WHERE j.id=$1`, key).Scan(&x.ProgramType, &x.UnitPriceMinor, &x.PreviousVersion, &x.PriceVersion, &x.IncludeExisting, &x.CreatedAt, &x.PreviousUnitPriceMinor, &x.AdminEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT i.brand_id,m.nombre,i.status,i.amount_minor FROM subscription_price_change_items i JOIN marcas m ON m.id=i.brand_id WHERE change_id=$1 ORDER BY m.nombre,i.brand_id`, key)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for rows.Next() {
		var item SubscriptionPriceChangeItem
		if err = rows.Scan(&item.BrandID, &item.BrandName, &item.Status, &item.AmountMinor); err != nil {
			return x, err
		}
		x.Items = append(x.Items, item)
	}
	return x, rows.Err()
}

func (r *Repository) RetrySubscriptionPriceChange(ctx context.Context, key uuid.UUID, adminID int64) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE subscription_price_change_items SET status='PENDING',updated_at=now() WHERE change_id=$1 AND status='FAILED'`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	if err = auditReferral(ctx, tx, adminID, "subscription_price.retry", "price_change", key.String()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// One durable item per transaction. Row locks coordinate billing webhooks,
// deletion and checkouts. Provider changes use a stable key for each charge index.
func (r *Repository) ApplyNextSubscriptionPriceChange(ctx context.Context, update func(context.Context, string, int64, string) error) (bool, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var key uuid.UUID
	var brand, amount, version int64
	var program, provider, external string
	err = tx.QueryRow(ctx, `SELECT i.change_id,i.brand_id,i.provider_subscription_id,i.external_reference,j.program_type,j.unit_price_minor,j.price_version
 FROM subscription_price_change_items i JOIN subscription_price_changes j ON j.id=i.change_id
 WHERE i.status='PENDING' AND NOT EXISTS(SELECT 1 FROM branch_operations bo WHERE bo.marca_id=i.brand_id AND bo.status IN('PAYMENT_PENDING','PLAN_UPDATING','REFUND_PENDING')) ORDER BY j.created_at,i.brand_id FOR UPDATE OF i SKIP LOCKED LIMIT 1`).Scan(&key, &brand, &provider, &external, &program, &amount, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(status string, monthly *int64) (bool, error) {
		if _, err := tx.Exec(ctx, `UPDATE subscription_price_change_items SET status=$3,amount_minor=$4,updated_at=now() WHERE change_id=$1 AND brand_id=$2`, key, brand, status, monthly); err != nil {
			return true, err
		}
		return true, tx.Commit(ctx)
	}
	var currentVersion int64
	if err = tx.QueryRow(ctx, `SELECT version FROM subscription_prices WHERE program_type=$1 FOR SHARE`, program).Scan(&currentVersion); err != nil {
		return true, err
	}
	if currentVersion != version {
		return finish("SKIPPED", nil)
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT activo AND deleted_at IS NULL FROM marcas WHERE id=$1 FOR UPDATE`, brand).Scan(&active); err != nil {
		return true, err
	}
	var actualProvider, actualExternal, status string
	var unit, full, branches int64
	var validFrom time.Time
	err = tx.QueryRow(ctx, `SELECT COALESCE(proveedor_suscripcion_id,''),referencia_externa,estado,precio_sucursal_minor,COALESCE(full_unit_price_minor,precio_sucursal_minor),cantidad_sucursales,COALESCE(price_valid_from,created_at) FROM suscripciones_marca WHERE marca_id=$1 FOR UPDATE`, brand).Scan(&actualProvider, &actualExternal, &status, &unit, &full, &branches, &validFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return finish("SKIPPED", nil)
	}
	if err != nil {
		return true, err
	}
	if pendingErr := requireNoPendingBranch(ctx, tx, brand); pendingErr != nil {
		if errors.Is(pendingErr, ErrIdempotencyInProgress) {
			return false, nil
		}
		return true, pendingErr
	}
	if !active || actualProvider != provider || actualExternal != external || (status != "PENDING" && status != "AUTHORIZED" && status != "PAUSED") {
		return finish("SKIPPED", nil)
	}
	var bps, remaining int
	var paid int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM referral_charges WHERE brand_id=$1`, brand).Scan(&paid); err != nil {
		return true, err
	}
	err = tx.QueryRow(ctx, `SELECT discount_bps,greatest(discount_charges-$2::bigint,0) FROM referral_attributions WHERE brand_id=$1`, brand, paid).Scan(&bps, &remaining)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return true, err
	}
	newUnit := amount
	if remaining > 0 {
		newUnit = discountedMinor(amount, bps)
	}
	if branches < 1 || amount > MaxSubscriptionAmountMinor/branches || newUnit < 1 {
		return finish("FAILED", nil)
	}
	monthly := newUnit * branches
	if unit == newUnit && full == amount {
		return finish("APPLIED", &monthly)
	}
	providerKey := fmt.Sprintf("price:%s:%d:%d", key.String(), brand, paid)
	if err = update(ctx, provider, monthly, providerKey); err != nil {
		// Retain failed items for an explicit retry; never claim provider success.
		_ = tx.Rollback(ctx)
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, saveErr := r.Pool.Exec(saveCtx, `UPDATE subscription_price_change_items SET status='FAILED',updated_at=now() WHERE change_id=$1 AND brand_id=$2 AND status='PENDING'`, key, brand)
		if saveErr != nil {
			return true, saveErr
		}
		return true, err
	}
	var changedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&changedAt); err != nil {
		return true, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO subscription_price_history(provider_subscription_id,unit_price_minor,full_unit_price_minor,branches,valid_from,valid_until,change_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, provider, unit, full, branches, validFrom, changedAt, key); err != nil {
		return true, err
	}
	if _, err = tx.Exec(ctx, `UPDATE suscripciones_marca SET precio_sucursal_minor=$2,full_unit_price_minor=$3,importe_mensual_minor=$4,program_type=$5,price_valid_from=$6,updated_at=$6 WHERE marca_id=$1`, brand, newUnit, amount, monthly, program, changedAt); err != nil {
		return true, err
	}
	return finish("APPLIED", &monthly)
}

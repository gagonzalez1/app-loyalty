package repository

import (
	"clientesFrecuentes/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

var ErrQuoteExpired = errors.New("branch quote expired")
var ErrQuoteChanged = errors.New("branch quote changed")

// BranchSnapshot omits volatile subscription updated_at: provider readbacks can refresh it.
type BranchSnapshot struct {
	ProviderID string     `json:"provider_id"`
	Reference  string     `json:"reference"`
	Unit       int64      `json:"unit"`
	FullUnit   int64      `json:"full_unit"`
	Monthly    int64      `json:"monthly"`
	Count      int64      `json:"count"`
	Next       time.Time  `json:"next"`
	Trial      *time.Time `json:"trial"`
	Status     string     `json:"status"`
	Anchor     int        `json:"anchor"`
}

func lockBranchBilling(ctx context.Context, tx pgx.Tx, actor, brand int64) (BranchSnapshot, error) {
	var out BranchSnapshot
	var role string
	err := tx.QueryRow(ctx, `SELECT mm.rol FROM marcas m JOIN membresias_marca mm ON mm.marca_id=m.id JOIN usuarios u ON u.id=mm.usuario_id AND u.activo AND u.deleted_at IS NULL WHERE m.id=$1 AND mm.usuario_id=$2 AND mm.activo AND m.activo AND m.deleted_at IS NULL FOR UPDATE OF m`, brand, actor).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && role != "PROPIETARIO" {
		return out, ErrForbidden
	}
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE(proveedor_suscripcion_id,''),referencia_externa,precio_sucursal_minor,COALESCE(full_unit_price_minor,precio_sucursal_minor),importe_mensual_minor,cantidad_sucursales,COALESCE(proximo_cobro_at,'epoch'::timestamptz),trial_ends_at,estado,EXTRACT(DAY FROM COALESCE(trial_ends_at,created_at) AT TIME ZONE 'America/Argentina/Buenos_Aires')::integer FROM suscripciones_marca WHERE marca_id=$1 FOR UPDATE`, brand).Scan(&out.ProviderID, &out.Reference, &out.Unit, &out.FullUnit, &out.Monthly, &out.Count, &out.Next, &out.Trial, &out.Status, &out.Anchor)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrSubscriptionChangeRequired
	}
	if err != nil {
		return out, err
	}
	var count int64
	err = tx.QueryRow(ctx, `SELECT count(*) FROM sucursales WHERE marca_id=$1 AND activo AND deleted_at IS NULL`, brand).Scan(&count)
	if err == nil && (out.Status != "AUTHORIZED" || out.ProviderID == "" || out.Unit < 0 || out.Count < 1 || count != out.Count || out.Monthly != out.Unit*out.Count) {
		err = ErrQuoteChanged
	}
	return out, err
}
func (r *Repository) SaveBranchQuote(ctx context.Context, actor, brand int64, draft model.CreateBranchRequest, quote func(BranchSnapshot) (model.BranchQuote, error)) (model.BranchQuote, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return model.BranchQuote{}, e
	}
	defer tx.Rollback(ctx)
	snapshot, e := lockBranchBilling(ctx, tx, actor, brand)
	if e != nil {
		return model.BranchQuote{}, e
	}
	q, e := quote(snapshot)
	if e != nil {
		return q, e
	}
	q.QuoteID = uuid.NewString()
	qb, _ := json.Marshal(q)
	db, _ := json.Marshal(draft)
	sb, _ := json.Marshal(snapshot)
	_, e = tx.Exec(ctx, `INSERT INTO branch_quotes(id,marca_id,actor_id,quote,draft,subscription_snapshot,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, q.QuoteID, brand, actor, qb, db, sb, q.ExpiresAt)
	if e != nil {
		return q, e
	}
	return q, tx.Commit(ctx)
}
func (r *Repository) ConfirmBranchQuote(ctx context.Context, actor, brand int64, quoteID, key string, sim bool, checkoutBase string, now time.Time) (model.BranchOperation, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return model.BranchOperation{}, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT m.id FROM marcas m JOIN membresias_marca mm ON mm.marca_id=m.id AND mm.usuario_id=$2 AND mm.activo AND mm.rol='PROPIETARIO' WHERE m.id=$1 AND m.activo AND m.deleted_at IS NULL FOR UPDATE OF m`, brand, actor)
	if e != nil {
		return model.BranchOperation{}, e
	}
	if _, e = r.BillingContext(ctx, actor, brand); e != nil {
		return model.BranchOperation{}, e
	}
	var existingID, existingQuote string
	e = tx.QueryRow(ctx, `SELECT id::text,quote_id::text FROM branch_operations WHERE marca_id=$1 AND idempotency_key=$2`, brand, key).Scan(&existingID, &existingQuote)
	if e == nil {
		if existingQuote != quoteID {
			return model.BranchOperation{}, ErrIdempotencyConflict
		}
		return readBranchOperation(ctx, tx, brand, existingID)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return model.BranchOperation{}, e
	}

	snapshot, e := lockBranchBilling(ctx, tx, actor, brand)
	if e != nil {
		return model.BranchOperation{}, e
	}
	var qb, sb []byte
	var expiry time.Time
	e = tx.QueryRow(ctx, `SELECT quote,subscription_snapshot,expires_at FROM branch_quotes WHERE id=$1 AND marca_id=$2 AND actor_id=$3`, quoteID, brand, actor).Scan(&qb, &sb, &expiry)
	if errors.Is(e, pgx.ErrNoRows) {
		return model.BranchOperation{}, ErrNotFound
	}
	if e != nil {
		return model.BranchOperation{}, e
	}
	if !expiry.After(now) {
		return model.BranchOperation{}, ErrQuoteExpired
	}
	current, _ := json.Marshal(snapshot)
	if string(current) != string(sb) {
		var stored BranchSnapshot
		if json.Unmarshal(sb, &stored) != nil || !branchSnapshotsEqual(snapshot, stored) {
			return model.BranchOperation{}, ErrQuoteChanged
		}
	}
	var q model.BranchQuote
	if e = json.Unmarshal(qb, &q); e != nil {
		return model.BranchOperation{}, e
	}
	id := uuid.NewString()
	status := "PAYMENT_PENDING"
	if !q.PaymentRequired {
		status = "PLAN_UPDATING"
	}
	checkout := ""
	if sim && q.PaymentRequired {
		checkout = checkoutBase + "?brand_id=" + branchInt(brand) + "&operation_id=" + id
	}
	_, e = tx.Exec(ctx, `INSERT INTO branch_operations(id,marca_id,actor_id,quote_id,idempotency_key,status,checkout_url,simulation,payment_amount_minor) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9)`, id, brand, actor, quoteID, key, status, checkout, sim, q.ProrationAmountCents)
	if IsUniqueViolation(e) {
		return model.BranchOperation{}, ErrIdempotencyInProgress
	}
	if e != nil {
		return model.BranchOperation{}, e
	}
	op, e := readBranchOperation(ctx, tx, brand, id)
	if e != nil {
		return op, e
	}
	return op, tx.Commit(ctx)
}
func branchSnapshotsEqual(a, b BranchSnapshot) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func branchInt(i int64) string { return fmt.Sprintf("%d", i) }

type branchQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readBranchOperation(ctx context.Context, p branchQuerier, brand int64, id string) (model.BranchOperation, error) {
	var o model.BranchOperation
	var qb []byte
	var branch *int64
	e := p.QueryRow(ctx, `SELECT o.id::text,o.status,o.simulation,q.quote,o.branch_id,COALESCE(o.checkout_url,''),o.error_code FROM branch_operations o JOIN branch_quotes q ON q.id=o.quote_id WHERE o.id=$1 AND o.marca_id=$2`, id, brand).Scan(&o.ID, &o.Status, &o.Simulated, &qb, &branch, &o.CheckoutURL, &o.ErrorCode)
	if errors.Is(e, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if e != nil {
		return o, e
	}
	if e = json.Unmarshal(qb, &o.Quote); e != nil {
		return o, e
	}
	if branch != nil {
		var b model.Branch
		e = scanBranch(p.QueryRow(ctx, `SELECT id,marca_id,nombre,direccion,activo,localidad,provincia,codigo_postal,latitud,longitud,principal,version,created_at,updated_at FROM sucursales WHERE id=$1`, *branch), &b)
		if e != nil {
			return o, e
		}
		o.Branch = &b
	}
	return o, nil
}
func (r *Repository) BranchOperation(ctx context.Context, actor, brand int64, id string) (model.BranchOperation, error) {
	if _, e := r.BillingContext(ctx, actor, brand); e != nil {
		return model.BranchOperation{}, e
	}
	return readBranchOperation(ctx, r.Pool, brand, id)
}

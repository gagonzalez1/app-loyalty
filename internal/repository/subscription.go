package repository

import (
	"context"
	"errors"

	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

type BillingContext struct {
	PayerEmail     string
	ActiveBranches int64
	ProgramType    string
}
type SubscriptionRecord struct {
	Subscription                  model.Subscription
	ProviderID, ExternalReference string
}

func (r *Repository) BillingContext(ctx context.Context, actorID, brandID int64) (BillingContext, error) {
	var out BillingContext
	err := r.Pool.QueryRow(ctx, `SELECT u.email::text,(SELECT count(*) FROM sucursales s WHERE s.marca_id=$2 AND s.activo AND s.deleted_at IS NULL),p.tipo FROM membresias_marca mm JOIN usuarios u ON u.id=mm.usuario_id JOIN marcas m ON m.id=mm.marca_id AND m.activo JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo AND mm.rol='PROPIETARIO'`, actorID, brandID).Scan(&out.PayerEmail, &out.ActiveBranches, &out.ProgramType)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrForbidden
	}
	if err != nil {
		return out, err
	}
	if out.ActiveBranches < 1 {
		return out, ErrInvalidRequest
	}
	return out, nil
}

func (r *Repository) GetSubscriptionRecord(ctx context.Context, brandID int64) (SubscriptionRecord, error) {
	var out SubscriptionRecord
	s := &out.Subscription
	err := r.Pool.QueryRow(ctx, `SELECT marca_id,proveedor,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at,proveedor_suscripcion_id,referencia_externa FROM suscripciones_marca WHERE marca_id=$1`, brandID).Scan(&s.BrandID, &s.Provider, &s.Status, &s.Currency, &s.UnitAmountCents, &s.ActiveBranches, &s.MonthlyAmountCents, &s.CheckoutURL, &s.NextPaymentDate, &s.UpdatedAt, &out.ProviderID, &out.ExternalReference)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func (r *Repository) SaveSubscriptionCheckout(ctx context.Context, brandID, unitPrice, branches int64, provider model.BillingSubscriptionResult) (model.Subscription, error) {
	status, ok := providerStatus(provider.Status)
	if !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	var out model.Subscription
	err := r.Pool.QueryRow(ctx, `INSERT INTO suscripciones_marca(marca_id,proveedor,proveedor_suscripcion_id,referencia_externa,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,checkout_url,proximo_cobro_at) VALUES($1,'MERCADO_PAGO',$2,$3,$4,'ARS',$5,$6,$5*$6,NULLIF($7,''),$8) ON CONFLICT(marca_id) DO UPDATE SET proveedor_suscripcion_id=EXCLUDED.proveedor_suscripcion_id,referencia_externa=EXCLUDED.referencia_externa,estado=EXCLUDED.estado,precio_sucursal_minor=EXCLUDED.precio_sucursal_minor,cantidad_sucursales=EXCLUDED.cantidad_sucursales,importe_mensual_minor=EXCLUDED.importe_mensual_minor,checkout_url=EXCLUDED.checkout_url,proximo_cobro_at=EXCLUDED.proximo_cobro_at,updated_at=now() RETURNING marca_id,proveedor,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at`, brandID, provider.ID, provider.ExternalReference, status, unitPrice, branches, provider.CheckoutURL, provider.NextPaymentDate).Scan(&out.BrandID, &out.Provider, &out.Status, &out.Currency, &out.UnitAmountCents, &out.ActiveBranches, &out.MonthlyAmountCents, &out.CheckoutURL, &out.NextPaymentDate, &out.UpdatedAt)
	return out, normalize(err)
}

func (r *Repository) RecordSubscriptionWebhook(ctx context.Context, notificationID, topic string, provider model.BillingSubscriptionResult) error {
	status, ok := providerStatus(provider.Status)
	if !ok {
		return ErrInvalidRequest
	}
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO eventos_mercado_pago(notification_id,resource_id,topic) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, notificationID, provider.ID, topic)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	tag, err = tx.Exec(ctx, `UPDATE suscripciones_marca SET estado=$2,checkout_url=COALESCE(NULLIF($3,''),checkout_url),proximo_cobro_at=$4,updated_at=now() WHERE proveedor_suscripcion_id=$1 AND referencia_externa=$5`, provider.ID, status, provider.CheckoutURL, provider.NextPaymentDate, provider.ExternalReference)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (r *Repository) UpdateSubscriptionFromProvider(ctx context.Context, provider model.BillingSubscriptionResult) (model.Subscription, error) {
	status, ok := providerStatus(provider.Status)
	if !ok {
		return model.Subscription{}, ErrInvalidRequest
	}
	var out model.Subscription
	err := r.Pool.QueryRow(ctx, `UPDATE suscripciones_marca SET estado=$2,checkout_url=COALESCE(NULLIF($3,''),checkout_url),proximo_cobro_at=$4,updated_at=now() WHERE proveedor_suscripcion_id=$1 AND referencia_externa=$5 RETURNING marca_id,proveedor,estado,moneda,precio_sucursal_minor,cantidad_sucursales,importe_mensual_minor,COALESCE(checkout_url,''),proximo_cobro_at,updated_at`, provider.ID, status, provider.CheckoutURL, provider.NextPaymentDate, provider.ExternalReference).Scan(&out.BrandID, &out.Provider, &out.Status, &out.Currency, &out.UnitAmountCents, &out.ActiveBranches, &out.MonthlyAmountCents, &out.CheckoutURL, &out.NextPaymentDate, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

func providerStatus(value string) (string, bool) {
	switch value {
	case "pending":
		return "PENDING", true
	case "authorized":
		return "AUTHORIZED", true
	case "paused":
		return "PAUSED", true
	case "cancelled", "canceled":
		return "CANCELLED", true
	default:
		return "", false
	}
}

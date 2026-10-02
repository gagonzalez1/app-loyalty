package repository

import (
	"clientesFrecuentes/internal/model"
	"context"
)

func (r *Repository) CancelSubscriptionLocked(ctx context.Context, actor, brand int64, cancel func(SubscriptionRecord) (model.BillingSubscriptionResult, error)) (model.Subscription, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return model.Subscription{}, e
	}
	defer tx.Rollback(ctx)
	var role string
	e = tx.QueryRow(ctx, `SELECT mm.rol FROM marcas m JOIN membresias_marca mm ON mm.marca_id=m.id WHERE m.id=$1 AND mm.usuario_id=$2 AND mm.activo AND m.activo AND m.deleted_at IS NULL FOR UPDATE OF m`, brand, actor).Scan(&role)
	if e != nil {
		return model.Subscription{}, e
	}
	if role != "PROPIETARIO" {
		return model.Subscription{}, ErrForbidden
	}
	_, e = tx.Exec(ctx, `SELECT marca_id FROM suscripciones_marca WHERE marca_id=$1 FOR UPDATE`, brand)
	if e != nil {
		return model.Subscription{}, e
	}
	if e = requireNoPendingBranch(ctx, tx, brand); e != nil {
		return model.Subscription{}, e
	}
	record, e := subscriptionRecord(ctx, tx, brand)
	if e != nil {
		return model.Subscription{}, e
	}
	out, e := cancel(record)
	if e != nil {
		return model.Subscription{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE suscripciones_marca SET estado='CANCELLED',checkout_url=NULL,proximo_cobro_at=NULL,updated_at=now() WHERE marca_id=$1 AND proveedor_suscripcion_id=$2 AND referencia_externa=$3`, brand, out.ID, out.ExternalReference)
	if e != nil {
		return model.Subscription{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return model.Subscription{}, e
	}
	record, e = r.GetSubscriptionRecord(ctx, brand)
	return record.Subscription, e
}

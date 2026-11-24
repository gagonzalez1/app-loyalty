package repository

import (
	"clientesFrecuentes/internal/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"time"
)

type AppleInboxItem struct {
	Environment, ID, Payload string
	Attempts                 int
}

func (r *Repository) StoreAppleNotification(ctx context.Context, n model.AppleNotification, signed string) error {
	if n.Data.Environment != r.appleEnvironment() {
		return ErrAppleEnvironmentConflict
	}
	payload, e := json.Marshal(map[string]string{"signedPayload": signed})
	if e != nil {
		return e
	}
	_, e = r.Pool.Exec(ctx, `INSERT INTO apple_notification_inbox(environment,notification_uuid,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, n.Data.Environment, n.NotificationUUID, payload)
	return e
}
func (r *Repository) LeaseAppleNotifications(ctx context.Context, environment string) ([]AppleInboxItem, error) {
	rows, e := r.Pool.Query(ctx, `WITH candidates AS (SELECT environment,notification_uuid FROM apple_notification_inbox WHERE environment=$1 AND completed_at IS NULL AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_attempt_at LIMIT 20 FOR UPDATE SKIP LOCKED) UPDATE apple_notification_inbox i SET lease_until=now()+interval '2 minutes',attempts=attempts+1 FROM candidates c WHERE i.environment=c.environment AND i.notification_uuid=c.notification_uuid RETURNING i.environment,i.notification_uuid::text,i.payload->>'signedPayload',i.attempts`, environment)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppleInboxItem, error) {
		var i AppleInboxItem
		e := row.Scan(&i.Environment, &i.ID, &i.Payload, &i.Attempts)
		return i, e
	})
}
func (r *Repository) CompleteAppleNotification(ctx context.Context, item AppleInboxItem, success bool) error {
	if success {
		_, e := r.Pool.Exec(ctx, `UPDATE apple_notification_inbox SET completed_at=now(),lease_until=NULL,last_error=NULL WHERE environment=$1 AND notification_uuid=$2`, item.Environment, item.ID)
		return e
	}
	retry := time.Duration(min(item.Attempts*item.Attempts, 3600)) * time.Second
	if retry < 10*time.Second {
		retry = 10 * time.Second
	}
	_, e := r.Pool.Exec(ctx, `UPDATE apple_notification_inbox SET next_attempt_at=now()+$3::interval,lease_until=NULL,last_error='reconciliation pending' WHERE environment=$1 AND notification_uuid=$2 AND completed_at IS NULL`, item.Environment, item.ID, retry.String())
	return e
}

type AppleReconcileChain struct {
	Environment, Original string
	Brand                 int64
}

func (r *Repository) LeaseAppleReconciliation(ctx context.Context, environment string) ([]AppleReconcileChain, error) {
	rows, e := r.Pool.Query(ctx, `WITH candidates AS (SELECT environment,original_transaction_id FROM apple_subscription_chains WHERE environment=$1 AND next_reconcile_at<=now() AND (reconcile_lease_until IS NULL OR reconcile_lease_until<now()) ORDER BY next_reconcile_at LIMIT 5 FOR UPDATE SKIP LOCKED) UPDATE apple_subscription_chains c SET reconcile_lease_until=now()+interval '2 minutes' FROM candidates p WHERE c.environment=p.environment AND c.original_transaction_id=p.original_transaction_id RETURNING c.environment,c.original_transaction_id,c.marca_id`, environment)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppleReconcileChain, error) {
		var c AppleReconcileChain
		e := row.Scan(&c.Environment, &c.Original, &c.Brand)
		return c, e
	})
}
func (r *Repository) FinishAppleReconciliation(ctx context.Context, c AppleReconcileChain, success bool) error {
	seconds := 60
	if success {
		seconds = 300
	}
	_, e := r.Pool.Exec(ctx, `UPDATE apple_subscription_chains SET reconcile_lease_until=NULL,next_reconcile_at=now()+make_interval(secs=>$3) WHERE environment=$1 AND original_transaction_id=$2`, c.Environment, c.Original, seconds)
	return e
}

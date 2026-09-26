package repository

import (
	"context"
	"time"
)

// SavePushToken keeps one token per device when device_id is supplied. A token
// registered by another account moves to the currently authenticated customer.
func (r *Repository) SavePushToken(ctx context.Context, customerID int64, token, deviceID string) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, customerID); err != nil {
		return err
	}
	if deviceID != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM push_tokens WHERE usuario_id=$1 AND device_id=$2 AND token<>$3`, customerID, deviceID, token); err != nil {
			return err
		}
	}
	// A transferred token must not receive a former owner's queued events.
	if _, err = tx.Exec(ctx, `DELETE FROM push_notifications WHERE token_id IN
		(SELECT id FROM push_tokens WHERE token=$1 AND usuario_id<>$2 FOR UPDATE)`, token, customerID); err != nil {
		return err
	}
	var nullableDevice any
	if deviceID != "" {
		nullableDevice = deviceID
	}
	if _, err = tx.Exec(ctx, `INSERT INTO push_tokens(usuario_id,device_id,token) VALUES($1,$2,$3)
		ON CONFLICT(token) DO UPDATE SET usuario_id=EXCLUDED.usuario_id,
		device_id=CASE WHEN push_tokens.usuario_id=EXCLUDED.usuario_id AND EXCLUDED.device_id IS NULL THEN push_tokens.device_id ELSE EXCLUDED.device_id END,
		activo=true,updated_at=now()`, customerID, nullableDevice, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PushJob is a leased outbox item. Never log Token.
type PushJob struct {
	ID         int64
	TokenID    int64
	CustomerID int64
	Token      string
	Attempts   int
}

func (r *Repository) ClaimPushJobs(ctx context.Context, limit int) ([]PushJob, error) {
	rows, err := r.Pool.Query(ctx, `WITH next AS (
		SELECT n.id FROM push_notifications n JOIN push_tokens t ON t.id=n.token_id AND t.activo
		WHERE (n.estado='PENDING' AND n.disponible_at<=now())
		   OR (n.estado='SENDING' AND n.lease_until<now())
		ORDER BY n.id LIMIT $1 FOR UPDATE OF n SKIP LOCKED
	), claimed AS (
		UPDATE push_notifications n SET estado='SENDING',intentos=intentos+1,lease_until=now()+interval '90 seconds',updated_at=now()
		FROM next WHERE n.id=next.id RETURNING n.id,n.token_id,n.intentos
	)
	SELECT c.id,c.token_id,t.usuario_id,t.token,c.intentos FROM claimed c JOIN push_tokens t ON t.id=c.token_id`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]PushJob, 0, limit)
	for rows.Next() {
		var job PushJob
		if err = rows.Scan(&job.ID, &job.TokenID, &job.CustomerID, &job.Token, &job.Attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *Repository) CompletePushJob(ctx context.Context, id int64, attempts int) error {
	_, err := r.Pool.Exec(ctx, `UPDATE push_notifications SET estado='SENT',lease_until=NULL,ultimo_error=NULL,updated_at=now() WHERE id=$1 AND estado='SENDING' AND intentos=$2`, id, attempts)
	return err
}

func (r *Repository) FailPushJob(ctx context.Context, id int64, attempts int, reason string) error {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	status := "PENDING"
	if attempts >= 5 {
		status = "FAILED"
	}
	delay := time.Duration(1<<min(attempts, 5)) * time.Minute
	_, err := r.Pool.Exec(ctx, `UPDATE push_notifications SET estado=$2,disponible_at=now()+($3 * interval '1 second'),lease_until=NULL,ultimo_error=$4,updated_at=now() WHERE id=$1 AND estado='SENDING' AND intentos=$5`, id, status, int(delay.Seconds()), reason, attempts)
	return err
}

func (r *Repository) DeleteInvalidPushToken(ctx context.Context, tokenID, customerID int64, token string) error {
	_, err := r.Pool.Exec(ctx, `DELETE FROM push_tokens WHERE id=$1 AND usuario_id=$2 AND token=$3`, tokenID, customerID, token)
	return err
}

func (r *Repository) CleanupPushJobs(ctx context.Context) error {
	_, err := r.Pool.Exec(ctx, `DELETE FROM push_notifications WHERE estado IN ('SENT','FAILED') AND updated_at<now()-interval '30 days'`)
	return err
}

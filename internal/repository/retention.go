package repository

import (
	"context"
	"time"
)

type RetentionPolicy struct {
	Now                  time.Time
	BatchSize            int
	PreviewRetention     time.Duration
	IdempotencyRetention time.Duration
	SessionRetention     time.Duration
	IdentityRetention    time.Duration
	OutboxRedactAfter    time.Duration
	OutboxRetention      time.Duration
}

type RetentionStats struct {
	PreviewsDeleted           int64
	IdempotenciesDeleted      int64
	SessionsDeleted           int64
	IdentityTokensDeleted     int64
	GoogleBrowserFlowsDeleted int64
	OutboxRedacted            int64
	OutboxDeleted             int64
}

func (r *Repository) ApplyRetention(ctx context.Context, p RetentionPolicy) (RetentionStats, error) {
	var stats RetentionStats
	statements := []struct {
		target *int64
		query  string
		cutoff time.Time
	}{
		{&stats.PreviewsDeleted, `WITH candidates AS (SELECT id FROM previews_movimiento WHERE expires_at<$1 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM previews_movimiento p USING candidates c WHERE p.id=c.id`, p.Now.Add(-p.PreviewRetention)},
		{&stats.IdempotenciesDeleted, `WITH candidates AS (SELECT idempotency_key FROM solicitudes_idempotentes WHERE estado='COMPLETED' AND completed_at<$1 ORDER BY completed_at,idempotency_key LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM solicitudes_idempotentes i USING candidates c WHERE i.idempotency_key=c.idempotency_key`, p.Now.Add(-p.IdempotencyRetention)},
		{&stats.SessionsDeleted, `WITH candidates AS (SELECT id FROM sesiones_auth WHERE expires_at<$1 OR (revoked_at IS NOT NULL AND revoked_at<$1) ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM sesiones_auth s USING candidates c WHERE s.id=c.id`, p.Now.Add(-p.SessionRetention)},
		{&stats.IdentityTokensDeleted, `WITH candidates AS (SELECT id FROM tokens_identidad_email WHERE expires_at<$1 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM tokens_identidad_email t USING candidates c WHERE t.id=c.id`, p.Now.Add(-p.IdentityRetention)},
		{&stats.GoogleBrowserFlowsDeleted, `WITH candidates AS (SELECT id FROM google_browser_flows WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM google_browser_flows f USING candidates c WHERE f.id=c.id`, p.Now},
	}
	for _, statement := range statements {
		tag, err := r.Pool.Exec(ctx, statement.query, statement.cutoff, p.BatchSize)
		if err != nil {
			return stats, err
		}
		*statement.target = tag.RowsAffected()
	}
	tag, err := r.Pool.Exec(ctx, `WITH candidates AS (SELECT id FROM email_outbox WHERE estado IN ('SENT','FAILED') AND redacted_at IS NULL AND created_at<$1 ORDER BY created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) UPDATE email_outbox e SET destinatario=('redacted-'||e.id::text||'@anon.invalid')::citext,asunto='redacted',cuerpo_texto=NULL,cuerpo_html=NULL,ultimo_error=NULL,token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,invitation_id=NULL,redacted_at=$3 WHERE e.id IN(SELECT id FROM candidates)`, p.Now.Add(-p.OutboxRedactAfter), p.BatchSize, p.Now)
	if err != nil {
		return stats, err
	}
	stats.OutboxRedacted = tag.RowsAffected()
	tag, err = r.Pool.Exec(ctx, `WITH candidates AS (SELECT id FROM email_outbox WHERE estado IN ('SENT','FAILED') AND created_at<$1 ORDER BY created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM email_outbox e USING candidates c WHERE e.id=c.id`, p.Now.Add(-p.OutboxRetention), p.BatchSize)
	if err != nil {
		return stats, err
	}
	stats.OutboxDeleted = tag.RowsAffected()
	return stats, nil
}

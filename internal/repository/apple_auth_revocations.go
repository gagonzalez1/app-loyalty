package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// queueAppleIdentityDeletion must run inside the account deletion transaction.
// It does not call Apple or retain the deleted account's subject/email in queue.
func queueAppleIdentityDeletion(ctx context.Context, tx pgx.Tx, userID int64, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO apple_identity_revocations(id,client_id,token_type,token_ciphertext,token_nonce,requested_at,available_at)
	 SELECT $2,client_id,token_type,token_ciphertext,token_nonce,$3,$3 FROM apple_identities WHERE usuario_id=$1`, userID, uuid.NewString(), now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM apple_identities WHERE usuario_id=$1`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM apple_auth_challenges WHERE usuario_id=$1`, userID)
	return err
}

type AppleRevocation struct {
	ID, ClientID, TokenType, LeaseOwner string
	Ciphertext, Nonce                   []byte
	Attempts                            int
}

func (r *Repository) ClaimAppleRevocations(ctx context.Context, limit int, now time.Time) ([]AppleRevocation, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	owner := uuid.NewString()
	rows, err := r.Pool.Query(ctx, `WITH due AS (
	 SELECT id FROM apple_identity_revocations WHERE available_at<=$1 AND (lease_until IS NULL OR lease_until<$1)
	 ORDER BY requested_at,id FOR UPDATE SKIP LOCKED LIMIT $2
	) UPDATE apple_identity_revocations r SET lease_owner=$3,lease_until=$1+interval '2 minutes',attempts=attempts+1
	 FROM due WHERE r.id=due.id RETURNING r.id::text,r.client_id,r.token_type,r.token_ciphertext,r.token_nonce,r.attempts`, now, limit, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AppleRevocation{}
	for rows.Next() {
		var item AppleRevocation
		item.LeaseOwner = owner
		if err = rows.Scan(&item.ID, &item.ClientID, &item.TokenType, &item.Ciphertext, &item.Nonce, &item.Attempts); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) CompleteAppleRevocation(ctx context.Context, item AppleRevocation) error {
	_, err := r.Pool.Exec(ctx, `DELETE FROM apple_identity_revocations WHERE id=$1 AND lease_owner=$2`, item.ID, item.LeaseOwner)
	return err
}

func (r *Repository) RetryAppleRevocation(ctx context.Context, item AppleRevocation, now time.Time) error {
	delay := time.Minute * time.Duration(1<<min(item.Attempts, 6))
	_, err := r.Pool.Exec(ctx, `UPDATE apple_identity_revocations SET available_at=$3,lease_owner=NULL,lease_until=NULL,last_error='provider revocation pending' WHERE id=$1 AND lease_owner=$2`, item.ID, item.LeaseOwner, now.Add(delay))
	return err
}

// PruneAppleChallenges bounds anonymous challenge retention. Consumed nonces
// need not outlive their expiry, and raw nonces are never stored at all.
func (r *Repository) PruneAppleChallenges(ctx context.Context, now time.Time) error {
	_, err := r.Pool.Exec(ctx, `DELETE FROM apple_auth_challenges WHERE expires_at<$1::timestamptz-interval '1 hour'`, now)
	return err
}

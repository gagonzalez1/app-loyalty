package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrGoogleBrowserFlow = errors.New("google browser flow unavailable")

func (r *Repository) CreateGoogleBrowserFlow(ctx context.Context, id string, nonceHash, verifierHash []byte, expiresAt time.Time) error {
	_, err := r.Pool.Exec(ctx, `INSERT INTO google_browser_flows(id,nonce_hash,verifier_hash,expires_at)
		VALUES($1,$2,$3,$4)`, id, nonceHash, verifierHash, expiresAt)
	return err
}

func (r *Repository) PendingGoogleBrowserNonce(ctx context.Context, id string, now time.Time) ([]byte, error) {
	var nonceHash []byte
	err := r.Pool.QueryRow(ctx, `SELECT nonce_hash FROM google_browser_flows
		WHERE id=$1 AND status='pending' AND expires_at>$2`, id, now).Scan(&nonceHash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrGoogleBrowserFlow
	}
	return nonceHash, err
}

func (r *Repository) ReceiveGoogleBrowserProof(ctx context.Context, id, status string, proof []byte, now time.Time) error {
	tag, err := r.Pool.Exec(ctx, `UPDATE google_browser_flows SET status=$2,proof=$3
		WHERE id=$1 AND status='pending' AND expires_at>$4`, id, status, proof, now)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrGoogleBrowserFlow
	}
	return err
}

// Claim consumes a finished flow exactly once and erases its encrypted proof.
// A verifier mismatch leaves the original browser's attempt untouched.
func (r *Repository) ClaimGoogleBrowserFlow(ctx context.Context, id string, verifierHash []byte, now time.Time) (string, []byte, error) {
	var status string
	var proof []byte
	err := r.Pool.QueryRow(ctx, `WITH finished AS MATERIALIZED (
		SELECT id,status,proof FROM google_browser_flows
		WHERE id=$1 AND verifier_hash=$2 AND expires_at>$3 AND status IN ('ready','cancelled','failed')
		FOR UPDATE
	) UPDATE google_browser_flows f SET status='used',proof=NULL
	FROM finished WHERE f.id=finished.id RETURNING finished.status,finished.proof`, id, verifierHash, now).Scan(&status, &proof)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrGoogleBrowserFlow
	}
	return status, proof, err
}

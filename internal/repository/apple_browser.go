package repository

import (
	"context"
	"errors"
	"time"

	"clientesFrecuentes/internal/appleidentity"
	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

type AppleBrowserFlow struct {
	ID, Platform, Status    string
	UserID                  int64
	AuthVersion             int
	NonceHash, VerifierHash []byte
	Proof, ProofNonce       []byte
	ExpiresAt               time.Time
}

func (r *Repository) CreateAppleBrowserFlow(ctx context.Context, f AppleBrowserFlow, sessionID string, now time.Time) error {
	tag, err := r.Pool.Exec(ctx, `INSERT INTO apple_browser_flows(id,platform,usuario_id,session_family,auth_version,nonce_hash,verifier_hash,expires_at)
	 SELECT $1,$2,NULLIF($3::bigint,0),s.family_id,NULLIF($9::integer,0),$4,$5,$6
	 FROM (SELECT 1) seed
	 LEFT JOIN sesiones_auth s ON s.id=NULLIF($7,'')::uuid AND s.usuario_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$8
	 WHERE $3=0 OR s.id IS NOT NULL`, f.ID, f.Platform, f.UserID, f.NonceHash, f.VerifierHash, f.ExpiresAt, sessionID, now, f.AuthVersion)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrAppleChallenge
	}
	return err
}

func (r *Repository) ReceiveAppleBrowserProof(ctx context.Context, id, status string, proof, proofNonce []byte, now time.Time) (string, error) {
	var platform string
	err := r.Pool.QueryRow(ctx, `UPDATE apple_browser_flows SET status=$2,proof=$3,proof_nonce=$4
	 WHERE id=$1 AND status='pending' AND expires_at>$5 RETURNING platform`, id, status, proof, proofNonce, now).Scan(&platform)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrAppleChallenge
	}
	return platform, err
}

// Claim consumes a completed flow before provider exchange. Pending flows
// can still receive their callback, and consumed proofs are removed immediately.
func (r *Repository) ClaimAppleBrowserFlow(ctx context.Context, id, platform string, userID int64, sessionID string, authVersion int, verifierHash []byte, now time.Time) (AppleBrowserFlow, error) {
	var f AppleBrowserFlow
	err := r.Pool.QueryRow(ctx, `WITH pending AS MATERIALIZED (
	 SELECT f.id,f.status,f.nonce_hash,f.proof,f.proof_nonce,f.session_family
	 FROM apple_browser_flows f WHERE f.id=$1 AND f.platform=$2 AND COALESCE(f.usuario_id,0)=$3
	 AND f.verifier_hash=$4 AND f.expires_at>$5 AND f.status IN ('pending','ready','cancelled','failed') AND COALESCE(f.auth_version,0)=$7
	 AND ($3=0 OR EXISTS(SELECT 1 FROM sesiones_auth s WHERE s.id=NULLIF($6,'')::uuid AND s.usuario_id=$3
	  AND s.family_id=f.session_family AND s.revoked_at IS NULL AND s.expires_at>$5))
	 FOR UPDATE OF f
	) UPDATE apple_browser_flows f SET status=CASE WHEN p.status='pending' THEN 'pending' ELSE 'used' END,proof=NULL,proof_nonce=NULL
	 FROM pending p WHERE f.id=p.id RETURNING p.status,p.nonce_hash,p.proof,p.proof_nonce`, id, platform, userID, verifierHash, now, sessionID, authVersion).
		Scan(&f.Status, &f.NonceHash, &f.Proof, &f.ProofNonce)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrAppleChallenge
	}
	return f, err
}

func (r *Repository) CompleteAppleBrowserAuth(ctx context.Context, subject string, tokens appleidentity.Tokens, userID int64, sessionID string, authVersion, accountVersion int, session AppleSession) (model.User, error) {
	token, err := r.encryptAppleTokens(tokens)
	if err != nil {
		return model.User{}, err
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)

	u, err := scanAppleUser(tx.QueryRow(ctx, `SELECT `+appleUserColumns+` FROM apple_identities a JOIN usuarios u ON u.id=a.usuario_id
	 WHERE a.subject=$1 FOR UPDATE OF u`, subject))
	if err != nil {
		return u, err
	}
	if !u.Active {
		return u, ErrForbidden
	}
	if userID != 0 {
		if u.ID != userID || u.AuthVersion != authVersion {
			return u, ErrAppleChallenge
		}
		if u.Version != accountVersion {
			return u, ErrPreconditionFailed
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sesiones_auth WHERE id=$1 AND usuario_id=$2 AND revoked_at IS NULL AND expires_at>$3)`, sessionID, userID, session.AuthTime).Scan(&active); err != nil {
			return u, err
		}
		if !active {
			return u, ErrAppleChallenge
		}
		if _, err = tx.Exec(ctx, `UPDATE usuarios SET auth_version=auth_version+1 WHERE id=$1`, userID); err != nil {
			return u, err
		}
		if _, err = tx.Exec(ctx, `UPDATE sesiones_auth SET revoked_at=COALESCE(revoked_at,$2) WHERE usuario_id=$1`, userID, session.AuthTime); err != nil {
			return u, err
		}
		u.AuthVersion++
	} else if err = recordFirstLogin(ctx, tx, u.ID, session.AuthTime); err != nil {
		return u, err
	}

	// A grouped Services ID shares the subject, but issues separate credentials.
	// Preserve a refresh token if Apple sends only an access token on a retry.
	if _, err = tx.Exec(ctx, `INSERT INTO apple_browser_credentials(usuario_id,client_id,token_type,token_ciphertext,token_nonce)
	 VALUES($1,$2,$3,$4,$5) ON CONFLICT(usuario_id,client_id) DO UPDATE
	 SET token_type=EXCLUDED.token_type,token_ciphertext=EXCLUDED.token_ciphertext,token_nonce=EXCLUDED.token_nonce
	 WHERE apple_browser_credentials.token_type<>'refresh_token' OR EXCLUDED.token_type='refresh_token'`, u.ID, token.clientID, token.kind, token.encrypted, token.nonce); err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sesiones_auth(id,usuario_id,refresh_hash,expires_at,family_id,auth_time)
	 VALUES($1,$2,$3,$4,$1,$5)`, session.ID, u.ID, session.RefreshHash, session.ExpiresAt, session.AuthTime); err != nil {
		return u, err
	}
	return u, tx.Commit(ctx)
}

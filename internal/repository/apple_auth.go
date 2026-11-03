package repository

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	"clientesFrecuentes/internal/appleidentity"
	"clientesFrecuentes/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrAppleChallenge        = errors.New("invalid apple challenge")
	ErrAppleLinkRequired     = errors.New("apple link required")
	ErrAppleIdentityConflict = errors.New("apple identity conflict")
)

type AppleChallengeRecord struct {
	ID, Purpose string
	UserID      int64
	SessionID   string
	NonceHash   []byte
	ExpiresAt   time.Time
}

func (r *Repository) CreateAppleChallenge(ctx context.Context, challenge AppleChallengeRecord, now time.Time) error {
	_, err := r.Pool.Exec(ctx, `INSERT INTO apple_auth_challenges(id,nonce_hash,purpose,usuario_id,session_id,created_at,expires_at)
		VALUES($1,$2,$3,NULLIF($4,0),NULLIF($5,'')::uuid,$6,$7)`, challenge.ID, challenge.NonceHash, challenge.Purpose, challenge.UserID, challenge.SessionID, now, challenge.ExpiresAt)
	return err
}

func (r *Repository) AppleChallenge(ctx context.Context, id, purpose string, userID int64, sessionID string, now time.Time) (AppleChallengeRecord, error) {
	if _, err := uuid.Parse(id); err != nil {
		return AppleChallengeRecord{}, ErrAppleChallenge
	}
	var out AppleChallengeRecord
	err := r.Pool.QueryRow(ctx, `SELECT id::text,purpose,COALESCE(usuario_id,0),COALESCE(session_id::text,''),nonce_hash,expires_at
	 FROM apple_auth_challenges WHERE id=$1 AND purpose=$2 AND COALESCE(usuario_id,0)=$3 AND COALESCE(session_id::text,'')=$4 AND consumed_at IS NULL AND expires_at>$5`, id, purpose, userID, sessionID, now).
		Scan(&out.ID, &out.Purpose, &out.UserID, &out.SessionID, &out.NonceHash, &out.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AppleChallengeRecord{}, ErrAppleChallenge
	}
	return out, err
}

func consumeAppleChallenge(ctx context.Context, tx pgx.Tx, challenge AppleChallengeRecord, now time.Time) error {
	var hash []byte
	err := tx.QueryRow(ctx, `UPDATE apple_auth_challenges SET consumed_at=$6 WHERE id=$1 AND purpose=$2 AND COALESCE(usuario_id,0)=$3
	 AND COALESCE(session_id::text,'')=$4 AND consumed_at IS NULL AND expires_at>$6 AND nonce_hash=$5 RETURNING nonce_hash`, challenge.ID, challenge.Purpose, challenge.UserID, challenge.SessionID, challenge.NonceHash, now).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAppleChallenge
	}
	if err != nil {
		return err
	}
	if len(hash) != sha256.Size || subtle.ConstantTimeCompare(hash, challenge.NonceHash) != 1 {
		return ErrAppleChallenge
	}
	return nil
}

const appleUserColumns = `u.id,u.email::text,u.nombre,u.apellido,u.alias,u.foto_url,u.tipo_cuenta,(u.activo AND u.deleted_at IS NULL),(u.email_verified_at IS NOT NULL),u.auth_version,u.version,u.created_at,COALESCE(u.codigo_usuario,'#USER-'||lpad(u.id::text,greatest(4,length(u.id::text)),'0'))`

func scanAppleUser(row pgx.Row) (model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt, &u.UserCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (r *Repository) ResolveAppleUser(ctx context.Context, subject, email string) (model.User, error) {
	u, err := scanAppleUser(r.Pool.QueryRow(ctx, `SELECT `+appleUserColumns+` FROM apple_identities a JOIN usuarios u ON u.id=a.usuario_id WHERE a.subject=$1`, subject))
	if err == nil {
		if !u.Active {
			return model.User{}, ErrForbidden
		}
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return model.User{}, err
	}
	if email != "" {
		existing, e := r.GetUserByEmail(ctx, email)
		if e == nil {
			if !existing.User.Active {
				return model.User{}, ErrForbidden
			}
			return model.User{}, ErrAppleLinkRequired
		}
		if !errors.Is(e, ErrNotFound) {
			return model.User{}, e
		}
	}
	return model.User{}, ErrNotFound
}

type AppleSignup struct {
	Name, LastName, AccountType string
	Merchant                    *model.GoogleMerchantRegistration
	FinalQRHash                 func(int64) []byte
}

type AppleSession struct {
	ID                  string
	RefreshHash         []byte
	ExpiresAt, AuthTime time.Time
}

type encryptedAppleToken struct {
	clientID, kind   string
	encrypted, nonce []byte
}

func (r *Repository) encryptAppleTokens(tokens appleidentity.Tokens) (encryptedAppleToken, error) {
	token, kind := tokens.Revocable()
	ciphertext, nonce, err := appleidentity.EncryptToken(r.OutboxCipherKey, tokens.ClientID, kind, token)
	return encryptedAppleToken{tokens.ClientID, kind, ciphertext, nonce}, err
}

func storeAppleIdentity(ctx context.Context, tx pgx.Tx, userID int64, identity appleidentity.Identity, token encryptedAppleToken, now time.Time) error {
	command, err := tx.Exec(ctx, `INSERT INTO apple_identities(usuario_id,subject,email,private_relay,client_id,token_type,token_ciphertext,token_nonce,created_at,updated_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
	 ON CONFLICT(usuario_id) DO UPDATE SET email=CASE WHEN EXCLUDED.email::text='' THEN apple_identities.email ELSE EXCLUDED.email END,
	 private_relay=CASE WHEN EXCLUDED.email::text='' THEN apple_identities.private_relay ELSE EXCLUDED.private_relay END,client_id=EXCLUDED.client_id,
	 token_type=CASE WHEN apple_identities.token_type='refresh_token' AND EXCLUDED.token_type='access_token' THEN apple_identities.token_type ELSE EXCLUDED.token_type END,
	 token_ciphertext=CASE WHEN apple_identities.token_type='refresh_token' AND EXCLUDED.token_type='access_token' THEN apple_identities.token_ciphertext ELSE EXCLUDED.token_ciphertext END,
	 token_nonce=CASE WHEN apple_identities.token_type='refresh_token' AND EXCLUDED.token_type='access_token' THEN apple_identities.token_nonce ELSE EXCLUDED.token_nonce END,updated_at=EXCLUDED.updated_at
	 WHERE apple_identities.subject=EXCLUDED.subject AND apple_identities.client_id=EXCLUDED.client_id`, userID, identity.Subject, identity.Email, identity.PrivateRelay, token.clientID, token.kind, token.encrypted, token.nonce, now)
	if IsUniqueViolation(err) {
		return ErrAppleIdentityConflict
	}
	if err == nil && command.RowsAffected() != 1 {
		return ErrAppleIdentityConflict
	}
	return err
}

// CompleteAppleLogin commits nonce consumption, signup, identity and session
// together. Remote code exchange is deliberately performed only after signup
// prerequisites are complete; the transaction rechecks all identity invariants.
func (r *Repository) CompleteAppleLogin(ctx context.Context, challenge AppleChallengeRecord, identity appleidentity.Identity, tokens appleidentity.Tokens, signup *AppleSignup, session AppleSession) (model.User, bool, error) {
	token, err := r.encryptAppleTokens(tokens)
	if err != nil {
		return model.User{}, false, err
	}
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.User{}, false, err
	}
	defer tx.Rollback(ctx)
	u, err := scanAppleUser(tx.QueryRow(ctx, `SELECT `+appleUserColumns+` FROM apple_identities a JOIN usuarios u ON u.id=a.usuario_id WHERE a.subject=$1 FOR UPDATE OF u`, identity.Subject))
	created := false
	if errors.Is(err, ErrNotFound) {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usuarios WHERE email=$1 AND deleted_at IS NULL)`, identity.Email).Scan(&exists); err != nil {
			return u, false, err
		}
		if exists {
			return u, false, ErrAppleLinkRequired
		}
		if signup == nil {
			return u, false, ErrNotFound
		}
		code, e := allocateUserCode(ctx, tx, signup.Name, signup.LastName)
		if e != nil {
			return u, false, e
		}
		var qr []byte
		if signup.AccountType == "CLIENTE_FINAL" {
			qr = make([]byte, 32)
		}
		u, err = scanAppleUser(tx.QueryRow(ctx, `INSERT INTO usuarios AS u(email,nombre,apellido,tipo_cuenta,qr_hash,email_verified_at,codigo_usuario)
		 VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+appleUserColumns, identity.Email, signup.Name, signup.LastName, signup.AccountType, qr, session.AuthTime, code))
		if err != nil {
			return u, false, normalize(err)
		}
		if signup.AccountType == "CLIENTE_FINAL" {
			if signup.FinalQRHash == nil {
				return u, false, ErrInvalidRequest
			}
			if _, err = tx.Exec(ctx, `UPDATE usuarios SET qr_hash=$2 WHERE id=$1`, u.ID, signup.FinalQRHash(u.ID)); err != nil {
				return u, false, err
			}
		} else {
			m := signup.Merchant
			if m == nil {
				return u, false, ErrInvalidRequest
			}
			location := model.BranchRegistrationLocation{BranchAddress: m.BranchAddress, BranchLocality: m.BranchLocality, BranchProvince: m.BranchProvince, BranchPostalCode: m.BranchPostalCode, BranchLatitude: m.BranchLatitude, BranchLongitude: m.BranchLongitude}
			merchant, e := createMerchantResources(ctx, tx, u.ID, m.BrandName, m.BranchName, location, m.ProgramType)
			if e != nil {
				return u, false, e
			}
			if err = AttributeReferral(ctx, tx, merchant.BrandID, m.ProgramType, m.ReferralCode); err != nil {
				return u, false, err
			}
		}
		created = true
	} else if err != nil {
		return u, false, err
	}
	if !u.Active {
		return u, false, ErrForbidden
	}
	if err = consumeAppleChallenge(ctx, tx, challenge, session.AuthTime); err != nil {
		return model.User{}, false, err
	}
	if err = storeAppleIdentity(ctx, tx, u.ID, identity, token, session.AuthTime); err != nil {
		return u, false, err
	}
	if err = recordFirstLogin(ctx, tx, u.ID, session.AuthTime); err != nil {
		return u, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sesiones_auth(id,usuario_id,refresh_hash,expires_at,family_id,auth_time) VALUES($1,$2,$3,$4,$1,$5)`, session.ID, u.ID, session.RefreshHash, session.ExpiresAt, session.AuthTime); err != nil {
		return u, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return u, false, normalize(err)
	}
	return u, created, nil
}

func (r *Repository) CompleteAppleAuthentication(ctx context.Context, challenge AppleChallengeRecord, identity appleidentity.Identity, tokens appleidentity.Tokens, userID int64, sessionID string, authVersion, accountVersion int, link bool, session AppleSession) (model.User, error) {
	token, err := r.encryptAppleTokens(tokens)
	if err != nil {
		return model.User{}, err
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	// Account lock order matches all authentication/session mutations.
	u, err := scanAppleUser(tx.QueryRow(ctx, `SELECT `+appleUserColumns+` FROM usuarios u WHERE u.id=$1 AND u.activo AND u.deleted_at IS NULL FOR UPDATE`, userID))
	if err != nil {
		return u, err
	}
	if u.AuthVersion != authVersion {
		return u, ErrNotFound
	}
	if accountVersion != 0 && u.Version != accountVersion {
		return u, ErrPreconditionFailed
	}
	var authTime time.Time
	err = tx.QueryRow(ctx, `SELECT auth_time FROM sesiones_auth WHERE id=$1 AND usuario_id=$2 AND revoked_at IS NULL AND expires_at>$3`, sessionID, userID, session.AuthTime).Scan(&authTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if link && (authTime.IsZero() || authTime.Before(session.AuthTime.Add(-10*time.Minute)) || authTime.After(session.AuthTime.Add(time.Minute))) {
		return u, ErrNotFound
	}
	var subject string
	err = tx.QueryRow(ctx, `SELECT subject FROM apple_identities WHERE usuario_id=$1 FOR UPDATE`, userID).Scan(&subject)
	if !errors.Is(err, pgx.ErrNoRows) && err != nil {
		return u, err
	}
	if (err == nil && subject != identity.Subject) || (!link && errors.Is(err, pgx.ErrNoRows)) {
		return u, ErrAppleIdentityConflict
	}
	if err = consumeAppleChallenge(ctx, tx, challenge, session.AuthTime); err != nil {
		return u, err
	}
	if err = storeAppleIdentity(ctx, tx, u.ID, identity, token, session.AuthTime); err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `UPDATE usuarios SET auth_version=auth_version+1,version=version+CASE WHEN $2 THEN 1 ELSE 0 END WHERE id=$1`, userID, link); err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sesiones_auth SET revoked_at=COALESCE(revoked_at,$2) WHERE usuario_id=$1`, userID, session.AuthTime); err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sesiones_auth(id,usuario_id,refresh_hash,expires_at,family_id,auth_time) VALUES($1,$2,$3,$4,$1,$5)`, session.ID, u.ID, session.RefreshHash, session.ExpiresAt, session.AuthTime); err != nil {
		return u, err
	}
	if err = tx.Commit(ctx); err != nil {
		return u, err
	}
	u.AuthVersion++
	if link {
		u.Version++
	}
	return u, nil
}

func (r *Repository) AppleSubjectForUser(ctx context.Context, userID int64) (string, error) {
	var subject string
	err := r.Pool.QueryRow(ctx, `SELECT a.subject FROM apple_identities a JOIN usuarios u ON u.id=a.usuario_id WHERE a.usuario_id=$1 AND u.activo AND u.deleted_at IS NULL`, userID).Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return subject, err
}

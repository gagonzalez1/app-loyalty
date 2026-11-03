package repository

import (
	"context"
	"errors"
	"time"

	"clientesFrecuentes/internal/model"

	"github.com/jackc/pgx/v5"
)

type AuthUser struct {
	User            model.User
	PasswordHash    *string
	GoogleID        *string
	EmailVerifiedAt *time.Time
}

func (r *Repository) CreateCustomer(ctx context.Context, email, passwordHash, name, lastName string, provisionalQRHash []byte, finalQRHash func(int64) []byte, verifiedAt *time.Time, tokenHash []byte, tokenExpires time.Time, message *model.EmailMessage) (model.User, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	code, err := allocateUserCode(ctx, tx, name, lastName)
	if err != nil {
		return model.User{}, err
	}
	var u model.User
	err = tx.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta,qr_hash,email_verified_at,apellido,codigo_usuario)
		VALUES($1,$2,$3,'CLIENTE_FINAL',$4,$5,$6,$7) RETURNING id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,(email_verified_at IS NOT NULL),auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0'))`, email, passwordHash, name, provisionalQRHash, verifiedAt, lastName, code).
		Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt, &u.UserCode)
	if err != nil {
		return model.User{}, normalize(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE usuarios SET qr_hash=$1 WHERE id=$2`, finalQRHash(u.ID), u.ID); err != nil {
		return model.User{}, err
	}
	if message != nil {
		if err = enqueueIdentityEmail(ctx, tx, r.OutboxCipherKey, u.ID, tokenHash, tokenExpires, *message); err != nil {
			return model.User{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return model.User{}, normalize(err)
	}
	return u, nil
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (AuthUser, error) {
	var u AuthUser
	err := r.Pool.QueryRow(ctx, `SELECT id,email::text,password_hash,google_id,nombre,apellido,alias,foto_url,tipo_cuenta,activo,email_verified_at,auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0')) FROM usuarios WHERE email=$1 AND deleted_at IS NULL`, email).
		Scan(&u.User.ID, &u.User.Email, &u.PasswordHash, &u.GoogleID, &u.User.Name, &u.User.LastName, &u.User.Alias, &u.User.PhotoURL, &u.User.AccountType, &u.User.Active, &u.EmailVerifiedAt, &u.User.AuthVersion, &u.User.Version, &u.User.CreatedAt, &u.User.UserCode)
	u.User.EmailVerified = u.EmailVerifiedAt != nil
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthUser{}, ErrNotFound
	}
	return u, err
}

func (r *Repository) LoginGoogle(ctx context.Context, googleID, email, name, lastName string, allowSignup bool, provisionalQRHash []byte, finalQRHash func(int64) []byte) (model.User, error) {
	u, err := r.ResolveGoogleUser(ctx, googleID, email)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return model.User{}, err
	}
	if !allowSignup {
		return model.User{}, ErrSignupDisabled
	}
	return r.CreateGoogleCustomer(ctx, googleID, email, name, lastName, provisionalQRHash, finalQRHash)
}

// ResolveGoogleUser resolves only an already associated subject. Email possession
// never authorizes linking or replacing another identity.
func (r *Repository) ResolveGoogleUser(ctx context.Context, googleID, email string) (model.User, error) {
	var u model.User
	err := r.Pool.QueryRow(ctx, `SELECT id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,(email_verified_at IS NOT NULL),auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0')) FROM usuarios WHERE google_id=$1 AND activo AND deleted_at IS NULL`, googleID).Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt, &u.UserCode)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, err
	}
	existing, err := r.GetUserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	if !existing.User.Active {
		return model.User{}, ErrForbidden
	}
	if existing.GoogleID != nil && *existing.GoogleID == googleID {
		return existing.User, nil
	}
	if existing.GoogleID != nil && *existing.GoogleID != googleID {
		return model.User{}, ErrGoogleIdentityConflict
	}
	return model.User{}, ErrGoogleLinkRequired
}

func (r *Repository) GetAuthUserByID(ctx context.Context, id int64) (AuthUser, error) {
	var u AuthUser
	err := r.Pool.QueryRow(ctx, `SELECT id,email::text,password_hash,google_id,nombre,apellido,alias,foto_url,tipo_cuenta,activo,email_verified_at,auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0')) FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL`, id).Scan(&u.User.ID, &u.User.Email, &u.PasswordHash, &u.GoogleID, &u.User.Name, &u.User.LastName, &u.User.Alias, &u.User.PhotoURL, &u.User.AccountType, &u.User.Active, &u.EmailVerifiedAt, &u.User.AuthVersion, &u.User.Version, &u.User.CreatedAt, &u.User.UserCode)
	u.User.EmailVerified = u.EmailVerifiedAt != nil
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthUser{}, ErrNotFound
	}
	return u, err
}

func (r *Repository) CreateGoogleCustomer(ctx context.Context, googleID, email, name, lastName string, provisionalQRHash []byte, finalQRHash func(int64) []byte) (model.User, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	code, err := allocateUserCode(ctx, tx, name, lastName)
	if err != nil {
		return model.User{}, err
	}
	var u model.User
	err = tx.QueryRow(ctx, `INSERT INTO usuarios(email,google_id,nombre,tipo_cuenta,qr_hash,email_verified_at,apellido,codigo_usuario) VALUES($1,$2,$3,'CLIENTE_FINAL',$4,now(),$5,$6) RETURNING id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,true,auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0'))`, email, googleID, name, provisionalQRHash, lastName, code).Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt, &u.UserCode)
	if err != nil {
		return model.User{}, normalize(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE usuarios SET qr_hash=$1 WHERE id=$2`, finalQRHash(u.ID), u.ID); err != nil {
		return model.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (r *Repository) GetUserByID(ctx context.Context, id int64) (model.User, error) {
	var u model.User
	err := r.Pool.QueryRow(ctx, `SELECT id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,(email_verified_at IS NOT NULL),auth_version,version,created_at,COALESCE(codigo_usuario,'#USER-'||lpad(id::text,greatest(4,length(id::text)),'0')) FROM usuarios WHERE id=$1 AND deleted_at IS NULL`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt, &u.UserCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	return u, err
}

func (r *Repository) UpdateAccount(ctx context.Context, id int64, expectedVersion int, req model.UpdateAccountRequest) (model.CurrentUser, error) {
	command, err := r.Pool.Exec(ctx, `UPDATE usuarios SET nombre=CASE WHEN $3 THEN $4 ELSE nombre END,
		apellido=CASE WHEN $5 THEN NULLIF($6,'') ELSE apellido END,
		alias=CASE WHEN $7 THEN NULLIF($8,'') ELSE alias END,
		version=version+1
		WHERE id=$1 AND version=$2 AND activo AND deleted_at IS NULL`, id, expectedVersion,
		req.Name.Set, patchValue(req.Name), req.LastName.Set, patchValue(req.LastName),
		req.Alias.Set, patchValue(req.Alias))
	if err != nil {
		return model.CurrentUser{}, err
	}
	if command.RowsAffected() != 1 {
		return model.CurrentUser{}, ErrPreconditionFailed
	}
	return r.GetCurrentUser(ctx, id)
}

func patchValue(value model.OptionalString) any {
	if value.Value == nil {
		return nil
	}
	return *value.Value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ActiveAccountType is the narrow, indexed authorization lookup used for every
// authenticated request. The primary-key lookup also enforces suspension and
// logical deletion before handlers can observe an actor.
func (r *Repository) ActiveAccountType(ctx context.Context, userID int64) (string, error) {
	var accountType string
	err := r.Pool.QueryRow(ctx, `SELECT tipo_cuenta FROM usuarios WHERE id=$1 AND activo AND deleted_at IS NULL`, userID).Scan(&accountType)
	return accountType, err
}

func (r *Repository) GetCurrentUser(ctx context.Context, id int64) (model.CurrentUser, error) {
	u, err := r.GetUserByID(ctx, id)
	if err != nil {
		return model.CurrentUser{}, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT m.id,m.nombre,mm.rol,COALESCE(array_agg(ms.sucursal_id ORDER BY ms.sucursal_id) FILTER(WHERE ms.activo),'{}')
		FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id
		LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id
		WHERE mm.usuario_id=$1 AND mm.activo AND m.activo GROUP BY m.id,m.nombre,mm.rol ORDER BY m.id`, id)
	if err != nil {
		return model.CurrentUser{}, err
	}
	defer rows.Close()
	memberships := make([]model.Membership, 0)
	for rows.Next() {
		var m model.Membership
		if err = rows.Scan(&m.BrandID, &m.BrandName, &m.Role, &m.BranchIDs); err != nil {
			return model.CurrentUser{}, err
		}
		m.Active = true
		memberships = append(memberships, m)
	}
	if err = rows.Err(); err != nil {
		return model.CurrentUser{}, err
	}
	onboardingComplete := u.AccountType == "CLIENTE_FINAL"
	if u.AccountType == "PERSONAL_MARCA" && len(memberships) > 0 {
		if err = r.Pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM membresias_marca mm
			JOIN programas_fidelidad p ON p.marca_id=mm.marca_id AND p.activo
			JOIN beneficios b ON b.programa_id=p.id AND b.activo
			WHERE mm.usuario_id=$1 AND mm.activo
		)`, id).Scan(&onboardingComplete); err != nil {
			return model.CurrentUser{}, err
		}
	}
	confirmed, err := r.AdultConfirmed(ctx, id)
	if err != nil {
		return model.CurrentUser{}, err
	}
	var methods model.AuthMethods
	if err = r.Pool.QueryRow(ctx, `SELECT password_hash IS NOT NULL,google_id IS NOT NULL,EXISTS(SELECT 1 FROM apple_identities a WHERE a.usuario_id=u.id) FROM usuarios u WHERE id=$1`, id).Scan(&methods.Password, &methods.Google, &methods.Apple); err != nil {
		return model.CurrentUser{}, err
	}
	return model.CurrentUser{User: u, Memberships: memberships, OnboardingComplete: onboardingComplete, AdultConfirmed: confirmed, AuthMethods: methods}, nil
}

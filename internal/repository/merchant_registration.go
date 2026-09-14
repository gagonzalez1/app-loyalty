package repository

import (
	"context"
	"time"

	"clientesFrecuentes/internal/model"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateDemoMerchant(ctx context.Context, key string, fingerprint []byte, email, passwordHash, ownerName, brandName, branchName string, branchAddress *string, programType, sessionID string, refreshHash []byte, sessionExpiresAt, authTime time.Time, verifiedAt *time.Time, verificationHash []byte, verificationExpires time.Time, message *model.EmailMessage, build func(model.User, model.MerchantContext) ([]byte, error)) (IdempotentResult, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IdempotentResult{}, err
	}
	defer tx.Rollback(ctx)
	actorScope := "demo-email:" + email
	claimed, err := claimIdempotency(ctx, tx, key, actorScope, "REGISTER_DEMO_MERCHANT", fingerprint)
	if err != nil {
		return IdempotentResult{}, err
	}
	if claimed != nil {
		return *claimed, nil
	}
	var u model.User
	err = tx.QueryRow(ctx, `INSERT INTO usuarios(email,password_hash,nombre,tipo_cuenta,email_verified_at) VALUES($1,$2,$3,'PERSONAL_MARCA',$4) RETURNING id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,(email_verified_at IS NOT NULL),auth_version,version,created_at`, email, passwordHash, ownerName, verifiedAt).
		Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt)
	if err != nil {
		return IdempotentResult{}, normalize(err)
	}
	merchant, err := createMerchantResources(ctx, tx, u.ID, brandName, branchName, branchAddress, programType)
	if err != nil {
		return IdempotentResult{}, err
	}
	if message != nil {
		if err = enqueueIdentityEmail(ctx, tx, r.OutboxCipherKey, u.ID, verificationHash, verificationExpires, *message); err != nil {
			return IdempotentResult{}, err
		}
	} else {
		if _, err = tx.Exec(ctx, `INSERT INTO sesiones_auth(id,usuario_id,refresh_hash,expires_at,family_id,auth_time) VALUES($1,$2,$3,$4,$1,$5)`, sessionID, u.ID, refreshHash, sessionExpiresAt, authTime); err != nil {
			return IdempotentResult{}, err
		}
	}
	body, err := build(u, merchant)
	if err != nil {
		return IdempotentResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE solicitudes_idempotentes SET estado='COMPLETED',response_status=201,response_body=$2,completed_at=now() WHERE idempotency_key=$1`, key, []byte(body)); err != nil {
		return IdempotentResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IdempotentResult{}, normalize(err)
	}
	return IdempotentResult{Status: 201, Body: body}, nil
}

func createMerchantResources(ctx context.Context, tx pgx.Tx, userID int64, brandName, branchName string, branchAddress *string, programType string) (model.MerchantContext, error) {
	var brandID, membershipID, branchID, programID int64
	var started time.Time
	if err := tx.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES($1) RETURNING id`, brandName).Scan(&brandID); err != nil {
		return model.MerchantContext{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO membresias_marca(usuario_id,marca_id,rol) VALUES($1,$2,'PROPIETARIO') RETURNING id`, userID, brandID).Scan(&membershipID); err != nil {
		return model.MerchantContext{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,direccion,principal) VALUES($1,$2,$3,true) RETURNING id`, brandID, branchName, branchAddress).Scan(&branchID); err != nil {
		return model.MerchantContext{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO membresias_sucursales(membresia_id,sucursal_id,marca_id) VALUES($1,$2,$3)`, membershipID, branchID, brandID); err != nil {
		return model.MerchantContext{}, err
	}
	var stampsPerAccumulation *int64
	if programType == "SELLOS" {
		one := int64(1)
		stampsPerAccumulation = &one
	}
	unitName := "sellos"
	if programType == "PUNTOS" {
		unitName = "puntos"
	}
	if err := tx.QueryRow(ctx, `INSERT INTO programas_fidelidad(marca_id,tipo,sellos_por_acumulacion,nombre_unidad) VALUES($1,$2,$3,$4) RETURNING id`, brandID, programType, stampsPerAccumulation, unitName).Scan(&programID); err != nil {
		return model.MerchantContext{}, err
	}
	demoKind := programType + "_FREE_TRIAL"
	if err := tx.QueryRow(ctx, `INSERT INTO accesos_demo(marca_id,tipo,precio_minor,moneda,cobro_automatico) VALUES($1,$2,0,'ARS',false) RETURNING started_at`, brandID, demoKind).Scan(&started); err != nil {
		return model.MerchantContext{}, err
	}
	return model.MerchantContext{
		BrandID: brandID, BrandName: brandName, Timezone: "America/Argentina/Buenos_Aires", BrandVersion: 1, Role: "PROPIETARIO",
		Branch:     model.Branch{ID: branchID, BrandID: brandID, Name: branchName, Address: branchAddress, Active: true, Primary: true, Version: 1},
		Program:    model.Program{ID: programID, BrandID: brandID, Type: programType, StampsPerAccumulation: stampsPerAccumulation, Active: true, UnitName: unitName, Version: 1},
		Benefits:   []model.Benefit{},
		DemoAccess: model.DemoAccess{Kind: demoKind, PriceMinor: 0, Currency: "ARS", AutomaticCharge: false, Active: true, StartedAt: started},
	}, nil
}

func (r *Repository) CreateGoogleMerchant(ctx context.Context, googleID, email, ownerName, brandName, branchName string, branchAddress *string, programType string) (model.User, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	var u model.User
	err = tx.QueryRow(ctx, `INSERT INTO usuarios(email,google_id,nombre,tipo_cuenta,email_verified_at) VALUES($1,$2,$3,'PERSONAL_MARCA',now()) RETURNING id,email::text,nombre,apellido,alias,foto_url,tipo_cuenta,activo,true,auth_version,version,created_at`, email, googleID, ownerName).
		Scan(&u.ID, &u.Email, &u.Name, &u.LastName, &u.Alias, &u.PhotoURL, &u.AccountType, &u.Active, &u.EmailVerified, &u.AuthVersion, &u.Version, &u.CreatedAt)
	if err != nil {
		return model.User{}, normalize(err)
	}
	if _, err = createMerchantResources(ctx, tx, u.ID, brandName, branchName, branchAddress, programType); err != nil {
		return model.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return model.User{}, normalize(err)
	}
	return u, nil
}

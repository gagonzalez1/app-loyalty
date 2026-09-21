package repository

import (
	"context"
	"errors"

	"clientesFrecuentes/internal/model"
	"github.com/jackc/pgx/v5"
)

type rowScanner interface{ Scan(...any) error }

func scanBenefit(row rowScanner, b *model.Benefit) error {
	return row.Scan(&b.ID, &b.ProgramID, &b.Name, &b.RequiredStamps, &b.RequiredPoints, &b.Active, &b.Version, &b.Description, &b.DeletedAt, &b.CreatedAt, &b.UpdatedAt)
}
func mutableRole(role string) bool { return role == "PROPIETARIO" || role == "ADMINISTRADOR" }

func (r *Repository) UpdateBrand(ctx context.Context, actorID, brandID int64, version int, req model.UpdateBrandRequest) (model.MerchantContext, error) {
	var role string
	err := r.Pool.QueryRow(ctx, `SELECT rol FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo`, actorID, brandID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.MerchantContext{}, ErrNotFound
	}
	if err != nil {
		return model.MerchantContext{}, err
	}
	if !mutableRole(role) {
		return model.MerchantContext{}, ErrForbidden
	}
	tag, err := r.Pool.Exec(ctx, `UPDATE marcas SET nombre=CASE WHEN $4 THEN $5 ELSE nombre END,descripcion=CASE WHEN $6 THEN NULLIF($7,'') ELSE descripcion END,color_primario=CASE WHEN $8 THEN NULLIF($9,'') ELSE color_primario END,color_secundario=CASE WHEN $10 THEN NULLIF($11,'') ELSE color_secundario END,zona_horaria=CASE WHEN $12 THEN $13 ELSE zona_horaria END,plantilla_tarjeta=CASE WHEN $14 THEN NULLIF($15,'') ELSE plantilla_tarjeta END,icono_premio=CASE WHEN $16 THEN NULLIF($17,'') ELSE icono_premio END,version=version+1,updated_at=now() WHERE $1::bigint IS NOT NULL AND id=$2 AND version=$3 AND activo AND deleted_at IS NULL`, actorID, brandID, version, req.Name != nil, stringValue(req.Name), req.Description != nil, stringValue(req.Description), req.PrimaryColor != nil, stringValue(req.PrimaryColor), req.SecondaryColor != nil, stringValue(req.SecondaryColor), req.Timezone != nil, stringValue(req.Timezone), req.CardTemplate != nil, stringValue(req.CardTemplate), req.RewardImage != nil, stringValue(req.RewardImage))
	if err != nil {
		return model.MerchantContext{}, err
	}
	if tag.RowsAffected() != 1 {
		return model.MerchantContext{}, ErrPreconditionFailed
	}
	return r.GetMerchantContext(ctx, actorID, brandID)
}
func (r *Repository) DeleteBrand(ctx context.Context, actorID, brandID int64, version int) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var role string
	err = tx.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id WHERE mm.usuario_id=$1 AND m.id=$2 AND mm.activo AND m.activo FOR UPDATE OF m`, actorID, brandID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !mutableRole(role) {
		return ErrForbidden
	}
	tag, err := tx.Exec(ctx, `UPDATE marcas SET activo=false,deleted_at=now(),version=version+1,updated_at=now() WHERE id=$1 AND version=$2`, brandID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrPreconditionFailed
	}
	if _, err = tx.Exec(ctx, `UPDATE email_outbox SET estado='FAILED',ultimo_error='brand deleted',token_ciphertext=NULL,token_nonce=NULL,token_expires_at=NULL,lease_until=NULL,lease_owner=NULL WHERE invitation_id IN(SELECT id FROM invitaciones_marca WHERE marca_id=$1 AND estado='PENDIENTE') AND estado IN('PENDING','SENDING')`, brandID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE invitaciones_marca SET estado='REVOCADA',version=version+1,updated_at=now() WHERE marca_id=$1 AND estado='PENDIENTE'`, brandID); err != nil {
		return err
	}
	for _, q := range []string{`UPDATE sucursales SET activo=false,deleted_at=COALESCE(deleted_at,now()),version=version+1,updated_at=now() WHERE marca_id=$1 AND activo`, `UPDATE programas_fidelidad SET activo=false,version=version+1,updated_at=now() WHERE marca_id=$1 AND activo`, `UPDATE beneficios SET activo=false,deleted_at=COALESCE(deleted_at,now()),version=version+1,updated_at=now() WHERE programa_id IN(SELECT id FROM programas_fidelidad WHERE marca_id=$1) AND activo`, `UPDATE tarjetas SET activo=false,deleted_at=COALESCE(deleted_at,now()),version=version+1 WHERE marca_id=$1 AND activo`, `UPDATE archivos_marca SET estado='DELETE_PENDING',delete_after=now()+interval '24 hours',lease_owner=NULL,lease_until=NULL,version=version+1,updated_at=now() WHERE marca_id=$1 AND estado IN ('ACTIVA','UPLOAD_PENDING','UPLOAD_FAILED')`, `UPDATE membresias_marca SET activo=false WHERE marca_id=$1`, `UPDATE accesos_demo SET activo=false WHERE marca_id=$1`} {
		if _, err = tx.Exec(ctx, q, brandID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) ListBranches(ctx context.Context, actorID, brandID int64) ([]model.Branch, error) {
	var role string
	if err := r.Pool.QueryRow(ctx, `SELECT rol FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo`, actorID, brandID).Scan(&role); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT s.id,s.marca_id,s.nombre,s.direccion,s.activo,s.localidad,s.provincia,s.codigo_postal,s.latitud,s.longitud,s.principal,s.version,s.created_at,s.updated_at FROM sucursales s WHERE s.marca_id=$1 AND ($2 IN ('PROPIETARIO','ADMINISTRADOR') OR EXISTS(SELECT 1 FROM membresias_marca mm JOIN membresias_sucursales ms ON ms.membresia_id=mm.id AND ms.activo WHERE mm.usuario_id=$3 AND mm.marca_id=$1 AND mm.activo AND ms.sucursal_id=s.id)) ORDER BY s.id`, brandID, role, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Branch{}
	for rows.Next() {
		var b model.Branch
		if err = scanBranch(rows, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func scanBranch(row rowScanner, b *model.Branch) error {
	return row.Scan(&b.ID, &b.BrandID, &b.Name, &b.Address, &b.Active, &b.Locality, &b.Province, &b.PostalCode, &b.Latitude, &b.Longitude, &b.Primary, &b.Version, &b.CreatedAt, &b.UpdatedAt)
}
func (r *Repository) GetBranch(ctx context.Context, actorID, brandID, branchID int64) (model.Branch, error) {
	var b model.Branch
	err := scanBranch(r.Pool.QueryRow(ctx, `SELECT s.id,s.marca_id,s.nombre,s.direccion,s.activo,s.localidad,s.provincia,s.codigo_postal,s.latitud,s.longitud,s.principal,s.version,s.created_at,s.updated_at FROM sucursales s JOIN membresias_marca mm ON mm.marca_id=s.marca_id AND mm.usuario_id=$1 AND mm.activo LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id AND ms.sucursal_id=s.id AND ms.activo WHERE s.id=$3 AND s.marca_id=$2 AND (mm.rol IN ('PROPIETARIO','ADMINISTRADOR') OR ms.sucursal_id IS NOT NULL)`, actorID, brandID, branchID), &b)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return b, err
}
func (r *Repository) CreateBranch(ctx context.Context, actorID, brandID int64, req model.CreateBranchRequest) (model.Branch, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.Branch{}, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err := tx.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id AND m.activo WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo FOR UPDATE OF m`, actorID, brandID).Scan(&role); err != nil {
		return model.Branch{}, ErrNotFound
	}
	if !mutableRole(role) {
		return model.Branch{}, ErrForbidden
	}
	var b model.Branch
	err = scanBranch(tx.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,direccion,localidad,provincia,codigo_postal,latitud,longitud) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,marca_id,nombre,direccion,activo,localidad,provincia,codigo_postal,latitud,longitud,principal,version,created_at,updated_at`, brandID, req.Name, req.Address, req.Locality, req.Province, req.PostalCode, req.Latitude, req.Longitude), &b)
	if err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}
func (r *Repository) UpdateBranch(ctx context.Context, actorID, brandID, branchID int64, version int, req model.UpdateBranchRequest) (model.Branch, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.Branch{}, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err = tx.QueryRow(ctx, `SELECT mm.rol FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo FOR UPDATE OF m`, actorID, brandID).Scan(&role); err != nil {
		return model.Branch{}, ErrNotFound
	}
	if !mutableRole(role) {
		return model.Branch{}, ErrForbidden
	}
	var currentVersion int
	if err = tx.QueryRow(ctx, `SELECT version FROM sucursales WHERE id=$1 AND marca_id=$2 AND activo FOR UPDATE`, branchID, brandID).Scan(&currentVersion); errors.Is(err, pgx.ErrNoRows) {
		return model.Branch{}, ErrNotFound
	} else if err != nil {
		return model.Branch{}, err
	}
	if currentVersion != version {
		return model.Branch{}, ErrPreconditionFailed
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM sucursales WHERE marca_id=$1 AND activo FOR UPDATE`, brandID); err != nil {
		return model.Branch{}, err
	}
	if req.Primary != nil && *req.Primary {
		if _, err = tx.Exec(ctx, `UPDATE sucursales SET principal=false,version=version+1,updated_at=now() WHERE marca_id=$1 AND principal AND activo AND id<>$2`, brandID, branchID); err != nil {
			return model.Branch{}, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE sucursales SET nombre=$3,direccion=$4,localidad=$5,provincia=$6,codigo_postal=$7,latitud=$8,longitud=$9,principal=CASE WHEN $10 THEN $11 ELSE principal END,version=version+1,updated_at=now() WHERE id=$1 AND marca_id=$2 AND version=$12 AND activo`, branchID, brandID, req.Name, req.Address, req.Locality, req.Province, req.PostalCode, req.Latitude, req.Longitude, req.Primary != nil, req.Primary, version)
	if err != nil {
		return model.Branch{}, err
	}
	if tag.RowsAffected() != 1 {
		return model.Branch{}, ErrPreconditionFailed
	}
	if req.Primary != nil && !*req.Primary {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM sucursales WHERE marca_id=$1 AND principal AND activo`, brandID).Scan(&count); err != nil {
			return model.Branch{}, err
		}
		if count == 0 {
			return model.Branch{}, ErrConflict
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Branch{}, err
	}
	return r.GetBranch(ctx, actorID, brandID, branchID)
}
func (r *Repository) DeleteBranch(ctx context.Context, actorID, brandID, branchID int64, version int) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var role string
	var primary bool
	var activeCount int
	err = tx.QueryRow(ctx, `SELECT mm.rol,s.principal,(SELECT count(*) FROM sucursales WHERE marca_id=$2 AND activo AND deleted_at IS NULL) FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id JOIN sucursales s ON s.marca_id=mm.marca_id WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo AND s.id=$3 AND s.activo FOR UPDATE OF m,s`, actorID, brandID, branchID).Scan(&role, &primary, &activeCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !mutableRole(role) {
		return ErrForbidden
	}
	if primary || activeCount <= 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM sucursales WHERE marca_id=$1 AND activo FOR UPDATE`, brandID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE sucursales SET activo=false,deleted_at=now(),version=version+1,updated_at=now() WHERE id=$1 AND marca_id=$2 AND version=$3 AND activo`, branchID, brandID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrPreconditionFailed
	}
	return tx.Commit(ctx)
}

func (r *Repository) GetProgram(ctx context.Context, actorID, brandID int64) (model.Program, error) {
	var p model.Program
	err := r.Pool.QueryRow(ctx, `SELECT id,marca_id,tipo,sellos_por_acumulacion,activo,nombre_unidad,version,created_at,updated_at FROM programas_fidelidad WHERE marca_id=$2 AND EXISTS(SELECT 1 FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo) ORDER BY id LIMIT 1`, actorID, brandID).Scan(&p.ID, &p.BrandID, &p.Type, &p.StampsPerAccumulation, &p.Active, &p.UnitName, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}
func (r *Repository) UpdateProgram(ctx context.Context, actorID, brandID int64, version int, req model.UpdateProgramRequest) (model.Program, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return model.Program{}, err
	}
	defer tx.Rollback(ctx)
	var role, current string
	var id int64
	err = tx.QueryRow(ctx, `SELECT mm.rol,p.id,p.tipo FROM membresias_marca mm JOIN programas_fidelidad p ON p.marca_id=mm.marca_id WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo FOR UPDATE OF p`, actorID, brandID).Scan(&role, &id, &current)
	if err != nil {
		return model.Program{}, ErrNotFound
	}
	if !mutableRole(role) {
		return model.Program{}, ErrForbidden
	}
	if current != req.Type {
		var moved, hasBenefits bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM historial_movimientos WHERE marca_id=$1),EXISTS(SELECT 1 FROM beneficios WHERE programa_id=$2)`, brandID, id).Scan(&moved, &hasBenefits); err != nil {
			return model.Program{}, err
		}
		if moved {
			return model.Program{}, ErrProgramTypeImmutable
		}
		if hasBenefits {
			return model.Program{}, ErrProgramTypeHasBenefits
		}
	}
	var stamps *int64
	if req.Type == "SELLOS" {
		one := int64(1)
		stamps = &one
	}
	tag, err := tx.Exec(ctx, `UPDATE programas_fidelidad SET tipo=$1,nombre_unidad=$2,activo=$3,sellos_por_acumulacion=$4,version=version+1,updated_at=now() WHERE id=$5 AND version=$6`, req.Type, req.UnitName, req.Active, stamps, id, version)
	if err != nil {
		return model.Program{}, err
	}
	if tag.RowsAffected() != 1 {
		return model.Program{}, ErrPreconditionFailed
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Program{}, err
	}
	return r.GetProgram(ctx, actorID, brandID)
}

func (r *Repository) GetBenefit(ctx context.Context, actorID, brandID, benefitID int64) (model.Benefit, error) {
	var b model.Benefit
	err := scanBenefit(r.Pool.QueryRow(ctx, `SELECT b.id,b.programa_id,b.nombre,b.requisito_sellos,b.requisito_puntos,b.activo,b.version,b.descripcion,b.deleted_at,b.created_at,b.updated_at FROM beneficios b JOIN programas_fidelidad p ON p.id=b.programa_id WHERE b.id=$3 AND p.marca_id=$2 AND EXISTS(SELECT 1 FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo)`, actorID, brandID, benefitID), &b)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return b, err
}
func (r *Repository) ReplaceBenefit(ctx context.Context, actorID, brandID, benefitID int64, version int, req model.ReplaceBenefitRequest) (model.Benefit, error) {
	var role, typ string
	err := r.Pool.QueryRow(ctx, `SELECT mm.rol,p.tipo FROM membresias_marca mm JOIN programas_fidelidad p ON p.marca_id=mm.marca_id WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo`, actorID, brandID).Scan(&role, &typ)
	if err != nil {
		return model.Benefit{}, ErrNotFound
	}
	if !mutableRole(role) {
		return model.Benefit{}, ErrForbidden
	}
	var currentVersion int
	if err = r.Pool.QueryRow(ctx, `SELECT b.version FROM beneficios b JOIN programas_fidelidad p ON p.id=b.programa_id WHERE b.id=$1 AND p.marca_id=$2`, benefitID, brandID).Scan(&currentVersion); errors.Is(err, pgx.ErrNoRows) {
		return model.Benefit{}, ErrNotFound
	} else if err != nil {
		return model.Benefit{}, err
	}
	if currentVersion != version {
		return model.Benefit{}, ErrPreconditionFailed
	}
	var stamps, points *int64
	if typ == "PUNTOS" {
		points = &req.Requirement
	} else {
		stamps = &req.Requirement
	}
	var b model.Benefit
	err = scanBenefit(r.Pool.QueryRow(ctx, `UPDATE beneficios b SET nombre=$1,descripcion=$2,requisito_sellos=$3,requisito_puntos=$4,activo=$5,deleted_at=CASE WHEN $5 THEN NULL ELSE COALESCE(b.deleted_at,now()) END,version=b.version+1,updated_at=now() FROM programas_fidelidad p WHERE b.id=$6 AND b.programa_id=p.id AND p.marca_id=$7 AND b.version=$8 RETURNING b.id,b.programa_id,b.nombre,b.requisito_sellos,b.requisito_puntos,b.activo,b.version,b.descripcion,b.deleted_at,b.created_at,b.updated_at`, req.Name, req.Description, stamps, points, req.Active, benefitID, brandID, version), &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrPreconditionFailed
	}
	return b, err
}
func (r *Repository) DeleteBenefit(ctx context.Context, actorID, brandID, benefitID int64, version int) error {
	b, err := r.GetBenefit(ctx, actorID, brandID, benefitID)
	if err != nil {
		return err
	}
	_ = b
	var role string
	if err = r.Pool.QueryRow(ctx, `SELECT rol FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo`, actorID, brandID).Scan(&role); err != nil {
		return err
	}
	if !mutableRole(role) {
		return ErrForbidden
	}
	tag, err := r.Pool.Exec(ctx, `UPDATE beneficios b SET activo=false,deleted_at=COALESCE(b.deleted_at,now()),version=b.version+1,updated_at=now() FROM programas_fidelidad p WHERE b.id=$1 AND b.programa_id=p.id AND p.marca_id=$2 AND b.version=$3`, benefitID, brandID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrPreconditionFailed
	}
	return nil
}

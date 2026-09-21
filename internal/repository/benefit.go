package repository

import (
	"context"
	"errors"

	"clientesFrecuentes/internal/model"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) ListBenefits(ctx context.Context, actorID, brandID int64) ([]model.Benefit, error) {
	var authorized bool
	if err := r.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM membresias_marca WHERE usuario_id=$1 AND marca_id=$2 AND activo)`, actorID, brandID).Scan(&authorized); err != nil {
		return nil, err
	}
	if !authorized {
		return nil, ErrNotFound
	}
	rows, err := r.Pool.Query(ctx, `SELECT b.id,b.programa_id,b.nombre,b.requisito_sellos,b.requisito_puntos,b.activo,b.version,b.descripcion,b.deleted_at,b.created_at,b.updated_at
		FROM beneficios b JOIN programas_fidelidad p ON p.id=b.programa_id AND p.marca_id=$1
		ORDER BY b.id`, brandID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Benefit, 0)
	for rows.Next() {
		var item model.Benefit
		if err = scanBenefit(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) listBenefitsByProgramIDs(ctx context.Context, programIDs []int64) (map[int64][]model.Benefit, error) {
	result := make(map[int64][]model.Benefit)
	if len(programIDs) == 0 {
		return result, nil
	}
	rows, err := r.Pool.Query(ctx, `SELECT b.id,b.programa_id,b.nombre,b.requisito_sellos,b.requisito_puntos,b.activo,b.version,b.descripcion,b.deleted_at,b.created_at,b.updated_at,
		COALESCE((SELECT a.object_key FROM archivos_marca a WHERE a.tipo='BENEFICIO' AND a.beneficio_id=b.id AND a.estado='ACTIVA' ORDER BY a.created_at DESC LIMIT 1),'')
		FROM beneficios b WHERE b.programa_id=ANY($1) AND b.activo AND b.deleted_at IS NULL ORDER BY b.id`, programIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item model.Benefit
		if err = rows.Scan(&item.ID, &item.ProgramID, &item.Name, &item.RequiredStamps, &item.RequiredPoints, &item.Active, &item.Version, &item.Description, &item.DeletedAt, &item.CreatedAt, &item.UpdatedAt, &item.ImageObjectKey); err != nil {
			return nil, err
		}
		result[item.ProgramID] = append(result[item.ProgramID], item)
	}
	return result, rows.Err()
}

func (r *Repository) CreateBenefit(ctx context.Context, actorID, brandID int64, name, description string, requirement int64) (model.Benefit, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.Benefit{}, err
	}
	defer tx.Rollback(ctx)
	var programID int64
	var programType, role string
	err = tx.QueryRow(ctx, `SELECT p.id,p.tipo,mm.rol FROM membresias_marca mm
		JOIN marcas m ON m.id=mm.marca_id AND m.activo AND m.deleted_at IS NULL
		JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo
		WHERE mm.usuario_id=$1 AND mm.marca_id=$2 AND mm.activo FOR SHARE OF mm,p`, actorID, brandID).Scan(&programID, &programType, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Benefit{}, ErrNotFound
	}
	if err != nil {
		return model.Benefit{}, err
	}
	if role != "PROPIETARIO" && role != "ADMINISTRADOR" {
		return model.Benefit{}, ErrForbidden
	}
	var requiredStamps, requiredPoints *int64
	if programType == "PUNTOS" {
		requiredPoints = &requirement
	} else {
		requiredStamps = &requirement
	}
	var item model.Benefit
	row := tx.QueryRow(ctx, `INSERT INTO beneficios(programa_id,nombre,descripcion,requisito_sellos,requisito_puntos)
		VALUES($1,$2,$3,$4,$5) RETURNING id,programa_id,nombre,requisito_sellos,requisito_puntos,activo,version,descripcion,deleted_at,created_at,updated_at`, programID, name, description, requiredStamps, requiredPoints)
	err = scanBenefit(row, &item)
	if err != nil {
		return model.Benefit{}, normalize(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Benefit{}, err
	}
	return item, nil
}

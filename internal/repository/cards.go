package repository

import (
	"context"

	"clientesFrecuentes/internal/model"
)

func (r *Repository) GetCustomer(ctx context.Context, actorID int64) (model.User, error) {
	u, err := r.GetUserByID(ctx, actorID)
	if err != nil {
		return model.User{}, err
	}
	if u.AccountType != "CLIENTE_FINAL" {
		return model.User{}, ErrNotFound
	}
	return u, nil
}

func (r *Repository) ListCards(ctx context.Context, actorID int64, page, pageSize int) ([]model.Card, int64, error) {
	var total int64
	if err := r.Pool.QueryRow(ctx, `SELECT count(*) FROM tarjetas WHERE usuario_id=$1 AND activo`, actorID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT t.id,m.id,m.nombre,m.color_primario,m.color_secundario,m.plantilla_tarjeta,m.icono_premio,
		COALESCE((SELECT a.object_key FROM archivos_marca a WHERE a.marca_id=m.id AND a.tipo='LOGO' AND a.estado='ACTIVA' ORDER BY a.created_at DESC LIMIT 1),''),
		p.tipo,t.saldo_sellos,t.saldo_puntos,b.id,b.programa_id,b.nombre,b.requisito_sellos,b.requisito_puntos,b.activo,b.version,
		COALESCE((SELECT a.object_key FROM archivos_marca a WHERE a.marca_id=m.id AND a.tipo='BENEFICIO' AND a.beneficio_id=b.id AND a.estado='ACTIVA' ORDER BY a.created_at DESC LIMIT 1),'')
		FROM tarjetas t JOIN marcas m ON m.id=t.marca_id JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo
		JOIN LATERAL (SELECT id,programa_id,nombre,requisito_sellos,requisito_puntos,activo,version FROM beneficios
			WHERE programa_id=p.id AND activo AND deleted_at IS NULL ORDER BY id LIMIT 1) b ON true
		WHERE t.usuario_id=$1 AND t.activo ORDER BY t.id LIMIT $2 OFFSET $3`, actorID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.Card, 0)
	programIDs := make([]int64, 0)
	for rows.Next() {
		var c model.Card
		if err = rows.Scan(&c.ID, &c.BrandID, &c.BrandName, &c.PrimaryColor, &c.SecondaryColor, &c.CardTemplate, &c.RewardImage, &c.BrandLogoObjectKey, &c.ProgramType, &c.BalanceStamps, &c.BalancePoints, &c.Benefit.ID, &c.Benefit.ProgramID, &c.Benefit.Name, &c.Benefit.RequiredStamps, &c.Benefit.RequiredPoints, &c.Benefit.Active, &c.Benefit.Version, &c.Benefit.ImageObjectKey); err != nil {
			return nil, 0, err
		}
		items = append(items, c)
		programIDs = append(programIDs, c.Benefit.ProgramID)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	benefitsByProgram, err := r.listBenefitsByProgramIDs(ctx, programIDs)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		items[i].Benefits = benefitsByProgram[items[i].Benefit.ProgramID]
	}
	return items, total, nil
}

package repository

import (
	"context"
	"errors"

	"clientesFrecuentes/internal/model"

	"github.com/jackc/pgx/v5"
)

const merchantContextSelect = `SELECT m.id,m.nombre,m.descripcion,m.color_primario,m.color_secundario,m.plantilla_tarjeta,m.icono_premio,m.zona_horaria,m.version,mm.rol,s.id,s.marca_id,s.nombre,s.direccion,s.activo,s.localidad,s.provincia,s.codigo_postal,s.latitud,s.longitud,s.principal,s.version,s.created_at,s.updated_at,
		p.id,p.marca_id,p.tipo,p.sellos_por_acumulacion,p.activo,p.nombre_unidad,p.version,p.created_at,p.updated_at,
	a.tipo,a.precio_minor,a.moneda,a.cobro_automatico,a.activo,a.started_at
	FROM membresias_marca mm JOIN marcas m ON m.id=mm.marca_id AND m.activo
	JOIN sucursales s ON s.marca_id=mm.marca_id AND s.activo
	LEFT JOIN membresias_sucursales ms ON ms.membresia_id=mm.id AND ms.marca_id=mm.marca_id AND ms.sucursal_id=s.id AND ms.activo
	JOIN programas_fidelidad p ON p.marca_id=m.id AND p.activo
	JOIN accesos_demo a ON a.marca_id=m.id AND a.activo
	WHERE mm.usuario_id=$1 AND mm.activo AND (mm.rol IN ('PROPIETARIO','ADMINISTRADOR') OR ms.sucursal_id IS NOT NULL)`

func scanMerchant(row pgx.Row) (model.MerchantContext, error) {
	var m model.MerchantContext
	err := row.Scan(&m.BrandID, &m.BrandName, &m.BrandDescription, &m.PrimaryColor, &m.SecondaryColor, &m.CardTemplate, &m.RewardImage, &m.Timezone, &m.BrandVersion, &m.Role, &m.Branch.ID, &m.Branch.BrandID, &m.Branch.Name, &m.Branch.Address, &m.Branch.Active, &m.Branch.Locality, &m.Branch.Province, &m.Branch.PostalCode, &m.Branch.Latitude, &m.Branch.Longitude, &m.Branch.Primary, &m.Branch.Version, &m.Branch.CreatedAt, &m.Branch.UpdatedAt,
		&m.Program.ID, &m.Program.BrandID, &m.Program.Type, &m.Program.StampsPerAccumulation, &m.Program.Active, &m.Program.UnitName, &m.Program.Version, &m.Program.CreatedAt, &m.Program.UpdatedAt,
		&m.DemoAccess.Kind, &m.DemoAccess.PriceMinor, &m.DemoAccess.Currency, &m.DemoAccess.AutomaticCharge, &m.DemoAccess.Active, &m.DemoAccess.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.MerchantContext{}, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.Benefits = make([]model.Benefit, 0)
	return m, nil
}

func attachBenefits(m *model.MerchantContext, byProgram map[int64][]model.Benefit) {
	m.Benefits = byProgram[m.Program.ID]
	if m.Benefits == nil {
		m.Benefits = make([]model.Benefit, 0)
	}
	if len(m.Benefits) > 0 {
		first := m.Benefits[0]
		m.Benefit = &first
	}
}

func (r *Repository) ListMerchantContexts(ctx context.Context, actorID int64) ([]model.MerchantContext, error) {
	rows, err := r.Pool.Query(ctx, merchantContextSelect+` ORDER BY m.id,s.id`, actorID)
	if err != nil {
		return nil, err
	}
	items := make([]model.MerchantContext, 0)
	programIDs := make([]int64, 0)
	seenPrograms := make(map[int64]bool)
	for rows.Next() {
		m, err := scanMerchant(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
		if !seenPrograms[m.Program.ID] {
			seenPrograms[m.Program.ID] = true
			programIDs = append(programIDs, m.Program.ID)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	benefits, err := r.listBenefitsByProgramIDs(ctx, programIDs)
	if err != nil {
		return nil, err
	}
	for i := range items {
		attachBenefits(&items[i], benefits)
	}
	return items, nil
}

func (r *Repository) GetMerchantContext(ctx context.Context, actorID, brandID int64) (model.MerchantContext, error) {
	m, err := scanMerchant(r.Pool.QueryRow(ctx, merchantContextSelect+` AND m.id=$2 ORDER BY s.id LIMIT 1`, actorID, brandID))
	if err != nil {
		return model.MerchantContext{}, err
	}
	benefits, err := r.listBenefitsByProgramIDs(ctx, []int64{m.Program.ID})
	if err != nil {
		return model.MerchantContext{}, err
	}
	attachBenefits(&m, benefits)
	return m, nil
}

func (r *Repository) ListBrandCustomers(ctx context.Context, actorID, brandID int64, page, pageSize int, search string) ([]model.BrandCustomer, int64, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	var authorized bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM usuarios u
		JOIN membresias_marca mm ON mm.usuario_id=u.id AND mm.activo
		JOIN marcas m ON m.id=mm.marca_id AND m.activo AND m.deleted_at IS NULL
		WHERE u.id=$1 AND u.tipo_cuenta='PERSONAL_MARCA' AND u.activo AND u.deleted_at IS NULL AND m.id=$2
	)`, actorID, brandID).Scan(&authorized)
	if err != nil {
		return nil, 0, err
	}
	if !authorized {
		return nil, 0, ErrNotFound
	}

	const cardsFrom = ` FROM tarjetas t
		JOIN usuarios u ON u.id=t.usuario_id AND u.tipo_cuenta='CLIENTE_FINAL' AND u.activo AND u.deleted_at IS NULL`
	const cardsWhere = ` WHERE t.marca_id=$1 AND t.activo AND t.deleted_at IS NULL
		AND ($2='' OR strpos(lower(u.nombre),lower($2))>0 OR strpos(lower(u.email::text),lower($2))>0)`
	var total int64
	if err = tx.QueryRow(ctx, `SELECT count(*)`+cardsFrom+cardsWhere, brandID, search).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := tx.Query(ctx, `SELECT u.id,t.id,u.nombre,u.email::text,p.tipo,t.saldo_sellos,t.saldo_puntos,
		count(h.id),max(h.occurred_at),t.created_at`+cardsFrom+`
		JOIN programas_fidelidad p ON p.marca_id=t.marca_id AND p.activo
		LEFT JOIN historial_movimientos h ON h.tarjeta_id=t.id AND h.marca_id=t.marca_id`+cardsWhere+`
		GROUP BY u.id,t.id,u.nombre,u.email,p.tipo,t.saldo_sellos,t.saldo_puntos,t.created_at
		ORDER BY max(h.occurred_at) DESC NULLS LAST,t.id DESC LIMIT $3 OFFSET $4`, brandID, search, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	items := make([]model.BrandCustomer, 0)
	for rows.Next() {
		var item model.BrandCustomer
		if err = rows.Scan(&item.CustomerID, &item.CardID, &item.Name, &item.Email, &item.ProgramType, &item.BalanceStamps, &item.BalancePoints, &item.MovementsCount, &item.LastMovementAt, &item.JoinedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *Repository) BrandMetricsSummary(ctx context.Context, actorID, brandID int64) (model.BrandMetricsSummary, error) {
	const query = `WITH authorized AS (
		SELECT 1 FROM usuarios u
		JOIN membresias_marca mm ON mm.usuario_id=u.id AND mm.activo AND mm.rol='PROPIETARIO'
		JOIN marcas m ON m.id=mm.marca_id AND m.activo AND m.deleted_at IS NULL
		WHERE u.id=$1 AND u.tipo_cuenta='PERSONAL_MARCA' AND u.activo AND u.deleted_at IS NULL AND m.id=$2
	), program AS (
		SELECT tipo FROM programas_fidelidad WHERE marca_id=$2 AND activo AND EXISTS(SELECT 1 FROM authorized)
	), active_cards AS (
		SELECT count(*) AS active_customers,COALESCE(sum(t.saldo_sellos),0)::bigint AS current_stamp_balance,
			COALESCE(sum(t.saldo_puntos),0)::bigint AS current_point_balance
		FROM tarjetas t JOIN usuarios u ON u.id=t.usuario_id AND u.tipo_cuenta='CLIENTE_FINAL' AND u.activo AND u.deleted_at IS NULL
		WHERE t.marca_id=$2 AND t.activo AND t.deleted_at IS NULL AND EXISTS(SELECT 1 FROM authorized)
	), ledger AS (
		SELECT count(*) FILTER(WHERE h.operacion='ACUMULACION') AS accumulations,
			count(*) FILTER(WHERE h.operacion='CANJE') AS redemptions,
			COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='SELLOS' AND h.operacion='ACUMULACION' AND h.sentido='CREDITO'),0)::bigint AS stamps_issued,
			COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='SELLOS' AND h.operacion='CANJE' AND h.sentido='DEBITO'),0)::bigint AS stamps_redeemed,
			COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='PUNTOS' AND h.operacion='ACUMULACION' AND h.sentido='CREDITO'),0)::bigint AS points_issued,
			COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='PUNTOS' AND h.operacion='CANJE' AND h.sentido='DEBITO'),0)::bigint AS points_redeemed,
			max(h.occurred_at) AS last_movement_at
		FROM historial_movimientos h WHERE h.marca_id=$2 AND EXISTS(SELECT 1 FROM authorized)
	)
	SELECT EXISTS(SELECT 1 FROM authorized),COALESCE((SELECT tipo FROM program),''),active_cards.active_customers,active_cards.current_stamp_balance,active_cards.current_point_balance,
		ledger.accumulations,ledger.redemptions,ledger.stamps_issued,ledger.stamps_redeemed,ledger.points_issued,ledger.points_redeemed,ledger.last_movement_at
	FROM active_cards CROSS JOIN ledger`
	var authorized bool
	var result model.BrandMetricsSummary
	err := r.Pool.QueryRow(ctx, query, actorID, brandID).Scan(&authorized, &result.ProgramType, &result.ActiveCustomers, &result.CurrentStampBalance, &result.CurrentPointBalance,
		&result.Accumulations, &result.Redemptions, &result.StampsIssued, &result.StampsRedeemed, &result.PointsIssued, &result.PointsRedeemed, &result.LastMovementAt)
	if err != nil {
		return model.BrandMetricsSummary{}, err
	}
	if !authorized {
		return model.BrandMetricsSummary{}, ErrNotFound
	}
	return result, nil
}

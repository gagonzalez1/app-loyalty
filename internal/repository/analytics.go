package repository

import (
	"clientesFrecuentes/internal/analytics"
	"clientesFrecuentes/internal/model"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// BrandPeriodMetrics reads authorization, totals and buckets from one snapshot.
// Customer/card status is deliberately not filtered: ledger history survives deletion.
func (r *Repository) BrandPeriodMetrics(ctx context.Context, actorID, brandID int64, period, date string, now time.Time) (model.BrandPeriodMetrics, error) {
	var out model.BrandPeriodMetrics
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var timezone, program string
	err = tx.QueryRow(ctx, `SELECT m.zona_horaria,COALESCE((SELECT tipo FROM programas_fidelidad WHERE marca_id=m.id AND activo),'')
 FROM usuarios u JOIN membresias_marca mm ON mm.usuario_id=u.id AND mm.activo AND mm.rol='PROPIETARIO'
 JOIN marcas m ON m.id=mm.marca_id AND m.activo AND m.deleted_at IS NULL
 WHERE u.id=$1 AND u.tipo_cuenta='PERSONAL_MARCA' AND u.activo AND u.deleted_at IS NULL AND m.id=$2`, actorID, brandID).Scan(&timezone, &program)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out, err = analytics.Resolve(period, date, timezone, now)
	if err != nil {
		return out, err
	}
	out.ProgramType = program
	err = tx.QueryRow(ctx, `SELECT count(DISTINCT t.usuario_id),
 count(*) FILTER(WHERE h.operacion='ACUMULACION'),count(*) FILTER(WHERE h.operacion='CANJE'),
 COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='SELLOS' AND h.operacion='ACUMULACION' AND h.sentido='CREDITO'),0)::bigint,
 COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='SELLOS' AND h.operacion='CANJE' AND h.sentido='DEBITO'),0)::bigint,
 COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='PUNTOS' AND h.operacion='ACUMULACION' AND h.sentido='CREDITO'),0)::bigint,
 COALESCE(sum(h.cantidad) FILTER(WHERE h.programa_tipo='PUNTOS' AND h.operacion='CANJE' AND h.sentido='DEBITO'),0)::bigint
 FROM historial_movimientos h JOIN tarjetas t ON t.id=h.tarjeta_id
 WHERE h.marca_id=$1 AND h.occurred_at >= $2 AND h.occurred_at < $3`, brandID, out.StartAt, out.EndAt).Scan(&out.CustomersServed, &out.Accumulations, &out.Redemptions, &out.StampsIssued, &out.StampsRedeemed, &out.PointsIssued, &out.PointsRedeemed)
	if err != nil {
		return out, err
	}
	format := "YYYY-MM-DD"
	if period == "day" {
		format = "HH24:00"
	}
	rows, err := tx.Query(ctx, `SELECT to_char(h.occurred_at AT TIME ZONE $4,$5) AS label,
 count(*) FILTER(WHERE h.operacion='ACUMULACION'),count(*) FILTER(WHERE h.operacion='CANJE')
 FROM historial_movimientos h WHERE h.marca_id=$1 AND h.occurred_at >= $2 AND h.occurred_at < $3
 GROUP BY label`, brandID, out.StartAt, out.EndAt, timezone, format)
	if err != nil {
		return out, err
	}
	buckets := make(map[string]model.BrandPeriodBucket, len(out.Series))
	for rows.Next() {
		var bucket model.BrandPeriodBucket
		if err = rows.Scan(&bucket.Label, &bucket.Accumulations, &bucket.Redemptions); err != nil {
			rows.Close()
			return out, err
		}
		buckets[bucket.Label] = bucket
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for i, bucket := range out.Series {
		if populated, ok := buckets[bucket.Label]; ok {
			out.Series[i] = populated
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

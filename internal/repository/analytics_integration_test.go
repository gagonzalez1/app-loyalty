package repository_test

import (
	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/internal/service"
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestPostgresPeriodMetrics(t *testing.T) {
	pool := reviewsDB(t)
	f := fixtureReviews(t, pool)
	ctx := context.Background()
	repo := repository.New(pool)
	svc := &service.Service{Repo: repo, Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }}
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`UPDATE marcas SET zona_horaria='America/Argentina/Buenos_Aires' WHERE id=$1`, f.brand)
	empty, err := svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "month", "2026-10-01")
	if err != nil || len(empty.Series) != 31 || empty.CustomersServed != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	insert := func(at, program, op string, amount int) {
		t.Helper()
		direction, before, after := "CREDITO", 0, amount
		if op == "CANJE" {
			direction, before, after = "DEBITO", amount, 0
		}
		must(`INSERT INTO historial_movimientos(operation_id,tarjeta_id,marca_id,sucursal_id,usuario_operador_id,operacion,sentido,cantidad,saldo_anterior,saldo_posterior,programa_tipo,occurred_at,marca_nombre_snapshot,sucursal_nombre_snapshot,programa_id_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'Brand','Main',$13)`, uuid.NewString(), f.card, f.brand, f.branch, f.owner, op, direction, amount, before, after, program, at, f.program)
	}
	insert("2026-09-28T00:00:00-03:00", "SELLOS", "ACUMULACION", 3)
	insert("2026-09-30T23:59:59-03:00", "PUNTOS", "ACUMULACION", 200)
	insert("2026-10-01T00:00:00-03:00", "SELLOS", "CANJE", 5)
	insert("2026-10-01T08:00:00-03:00", "PUNTOS", "CANJE", 100)
	insert("2026-10-05T00:00:00-03:00", "SELLOS", "ACUMULACION", 99) // exclusive boundary
	insert("2026-09-27T23:59:59-03:00", "SELLOS", "ACUMULACION", 99) // preceding week
	// A second customer served in another branch is included once at brand scope.
	var secondBranch, secondCard int64
	if err = pool.QueryRow(ctx, `INSERT INTO sucursales(marca_id,nombre,direccion) VALUES($1,'Other','Street 43') RETURNING id`, f.brand).Scan(&secondBranch); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO tarjetas(usuario_id,marca_id) VALUES($1,$2) RETURNING id`, f.other, f.brand).Scan(&secondCard); err != nil {
		t.Fatal(err)
	}
	originalCard, originalBranch := f.card, f.branch
	f.card, f.branch = secondCard, secondBranch
	insert("2026-09-29T12:00:00-03:00", "SELLOS", "ACUMULACION", 7)
	f.card, f.branch = originalCard, originalBranch
	// Another brand's ledger must not leak into the selected brand.
	otherBrand := fixtureReviews(t, pool)
	insertReviewMovement(t, pool, otherBrand, true)
	must(`UPDATE usuarios SET activo=false,deleted_at=now() WHERE id=$1`, f.customer)
	must(`UPDATE tarjetas SET activo=false,deleted_at=now() WHERE id=$1`, f.card)
	out, err := svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "week", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if out.CustomersServed != 2 || out.Accumulations != 3 || out.Redemptions != 2 || out.StampsIssued != 10 || out.StampsRedeemed != 5 || out.PointsIssued != 200 || out.PointsRedeemed != 100 || len(out.Series) != 7 || out.ProgramType != "PUNTOS" {
		t.Fatalf("totals=%+v", out)
	}
	if out.Series[0].Accumulations != 1 || out.Series[1].Accumulations != 1 || out.Series[2].Accumulations != 1 || out.Series[3].Redemptions != 2 || out.Series[4].Accumulations != 0 {
		t.Fatalf("series=%+v", out.Series)
	}
	day, err := svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", "")
	if err != nil || day.Date != "2026-10-01" || day.Redemptions != 2 || day.Accumulations != 0 || day.Series[0].Redemptions != 1 || day.Series[8].Redemptions != 1 {
		t.Fatalf("day=%+v err=%v", day, err)
	}
	for _, actor := range []int64{f.operator, f.customer, f.other} {
		if _, err = svc.BrandPeriodMetrics(ctx, actor, f.brand, "day", ""); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("actor=%d err=%v", actor, err)
		}
	}
	if _, err = svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", "2026-10-02"); !errors.Is(err, service.ErrInvalidRequest) {
		t.Fatalf("future err=%v", err)
	}
	must(`UPDATE membresias_marca SET activo=false WHERE usuario_id=$1`, f.owner)
	if _, err = svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("stale owner err=%v", err)
	}
	must(`UPDATE membresias_marca SET activo=true WHERE usuario_id=$1`, f.owner)
	must(`UPDATE usuarios SET activo=false WHERE id=$1`, f.owner)
	if _, err = svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("inactive owner err=%v", err)
	}
	// DST repeated hours combine into a single label; missing hours remain zero.
	must(`UPDATE usuarios SET activo=true WHERE id=$1`, f.owner)
	must(`UPDATE marcas SET zona_horaria='America/New_York' WHERE id=$1`, f.brand)
	insert("2025-11-02T01:30:00-04:00", "SELLOS", "ACUMULACION", 1)
	insert("2025-11-02T01:30:00-05:00", "SELLOS", "ACUMULACION", 1)
	fall, err := svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", "2025-11-02")
	if err != nil || fall.Accumulations != 2 || fall.Series[1].Accumulations != 2 || len(fall.Series) != 24 {
		t.Fatalf("fall=%+v err=%v", fall, err)
	}
	insert("2026-03-08T01:30:00-05:00", "PUNTOS", "ACUMULACION", 100)
	insert("2026-03-08T03:30:00-04:00", "PUNTOS", "ACUMULACION", 100)
	spring, err := svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", "2026-03-08")
	if err != nil || spring.PointsIssued != 200 || spring.Series[2].Accumulations != 0 || len(spring.Series) != 24 {
		t.Fatalf("spring=%+v err=%v", spring, err)
	}
	must(`UPDATE marcas SET zona_horaria='America/Argentina/Buenos_Aires' WHERE id=$1`, f.brand)
	insert("2008-10-19T01:00:00-02:00", "SELLOS", "ACUMULACION", 1)
	insert("2008-10-18T23:59:59-03:00", "SELLOS", "ACUMULACION", 99)
	midnight, err := svc.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", "2008-10-19")
	if err != nil || midnight.StampsIssued != 1 || midnight.Series[0].Accumulations != 0 || midnight.Series[1].Accumulations != 1 || midnight.StartAt.Format(time.RFC3339) != "2008-10-19T01:00:00-02:00" {
		t.Fatalf("midnight=%+v err=%v", midnight, err)
	}

}

func TestPostgresPeriodMetricsSellosProgram(t *testing.T) {
	pool := reviewsDB(t)
	f := fixtureReviews(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE programas_fidelidad SET tipo='SELLOS',sellos_por_acumulacion=1 WHERE id=$1`, f.program); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(pool)
	out, err := repo.BrandPeriodMetrics(ctx, f.owner, f.brand, "day", "2026-10-01", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil || out.ProgramType != "SELLOS" || out.PointsIssued != 0 || out.StampsIssued != 0 || out.CustomersServed != 0 || len(out.Series) != 24 {
		t.Fatalf("sellos=%+v err=%v", out, err)
	}
}

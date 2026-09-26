package repository_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clientesFrecuentes/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPushTokenDevicesAndOwnership(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS citext`); err != nil {
		t.Fatal(err)
	}
	schema := "test_push_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)

	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// This focused fixture only needs the artwork columns used by the trigger.
	if _, err := pool.Exec(ctx, `CREATE TABLE archivos_marca(marca_id BIGINT, tipo TEXT, estado TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_demo_sellos.up.sql", "0021_expo_push.up.sql"} {
		sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		migration := strings.Replace(string(sql), "CREATE EXTENSION IF NOT EXISTS citext;", "", 1)
		if _, err := pool.Exec(ctx, migration); err != nil {
			t.Fatalf("migration %s: %v", name, err)
		}
	}

	var alice, bob int64
	if err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash) VALUES('push-alice@example.com','Alice','CLIENTE_FINAL',decode('01','hex')) RETURNING id`).Scan(&alice); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO usuarios(email,nombre,tipo_cuenta,qr_hash) VALUES('push-bob@example.com','Bob','CLIENTE_FINAL',decode('02','hex')) RETURNING id`).Scan(&bob); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(pool)
	first := "ExpoPushToken[alice-device-one]"
	second := "ExpoPushToken[alice-device-two]"
	replacement := "ExpoPushToken[alice-device-one-new]"
	if err := repo.SavePushToken(ctx, alice, first, "device-one"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePushToken(ctx, alice, second, "device-two"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePushToken(ctx, alice, replacement, "device-one"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePushToken(ctx, alice, replacement, "device-one"); err != nil {
		t.Fatal(err)
	}
	var total, firstCount, secondCount, replacementCount int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE token=$2),count(*) FILTER (WHERE token=$3),count(*) FILTER (WHERE token=$4) FROM push_tokens WHERE usuario_id=$1 AND activo`, alice, first, second, replacement).Scan(&total, &firstCount, &secondCount, &replacementCount); err != nil {
		t.Fatal(err)
	}
	if total != 2 || firstCount != 0 || secondCount != 1 || replacementCount != 1 {
		t.Fatalf("device update: total=%d first=%d second=%d replacement=%d", total, firstCount, secondCount, replacementCount)
	}
	var brandID int64
	if err := pool.QueryRow(ctx, `INSERT INTO marcas(nombre) VALUES('Push test brand') RETURNING id`).Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	cardTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cardID int64
	if err := cardTx.QueryRow(ctx, `INSERT INTO tarjetas(usuario_id,marca_id) VALUES($1,$2) RETURNING id`, alice, brandID).Scan(&cardID); err != nil {
		cardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := cardTx.Exec(ctx, `UPDATE tarjetas SET version=version+1 WHERE id=$1`, cardID); err != nil {
		cardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := cardTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var queued, distinctTokens int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT token_id) FROM push_notifications`).Scan(&queued, &distinctTokens); err != nil {
		t.Fatal(err)
	}
	if queued != 2 || distinctTokens != 2 {
		t.Fatalf("insert and update in one transaction: queued=%d distinct_tokens=%d, want one per device", queued, distinctTokens)
	}

	rollbackTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackTx.Exec(ctx, `UPDATE tarjetas SET version=version+1 WHERE id=$1`, cardID); err != nil {
		rollbackTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_notifications`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("rolled-back card update changed queue size: got %d, want 2", queued)
	}
	if _, err := pool.Exec(ctx, `UPDATE marcas SET nombre='Renamed push test brand' WHERE id=$1`, brandID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_notifications`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 4 {
		t.Fatalf("brand change queued=%d notifications, want 4", queued)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM push_notifications`); err != nil {
		t.Fatal(err)
	}

	// A token transferred to another account must stop receiving Alice's updates.
	if _, err := pool.Exec(ctx, `INSERT INTO push_notifications(token_id) SELECT id FROM push_tokens WHERE token=$1`, second); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePushToken(ctx, bob, second, "bob-device"); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err := pool.QueryRow(ctx, `SELECT usuario_id FROM push_tokens WHERE token=$1`, second).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != bob {
		t.Fatalf("transferred token owner=%d, want %d", owner, bob)
	}
	jobs, err := repo.ClaimPushJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("old owner's queued notification survived token transfer: %+v", jobs)
	}
}

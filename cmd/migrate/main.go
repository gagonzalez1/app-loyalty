package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"clientesFrecuentes/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down" && os.Args[1] != "reconcile-testing-apple-0045") {
		fatal("usage: migrate up|down|reconcile-testing-apple-0045")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fatal("DATABASE_URL is required")
	}
	pc, err := config.ParsePoolConfig(databaseURL)
	if err != nil {
		fatal(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		fatal(err.Error())
	}
	defer pool.Close()
	if os.Args[1] == "reconcile-testing-apple-0045" {
		err = reconcileTestingApple0045(ctx, pool, os.Getenv("APP_ENV"))
	} else {
		err = run(ctx, pool, os.Args[1], envDefault("MIGRATIONS_DIR", "migrations"))
	}
	if err != nil {
		fatal(err.Error())
	}
}

func run(ctx context.Context, pool *pgxpool.Pool, direction, dir string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(73194211)`); err != nil {
		return err
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unlockCancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(73194211)`)
	}()
	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version CHAR(4) PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	suffix := ".up.sql"
	if direction == "down" {
		suffix = ".down.sql"
	}
	files := make([]string, 0)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) && len(e.Name()) >= 4 {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if direction == "down" {
		if os.Getenv("ALLOW_MIGRATION_DOWN") != "true" {
			return fmt.Errorf("down migration requires ALLOW_MIGRATION_DOWN=true")
		}
		var version string
		if err = conn.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT 1`).Scan(&version); err != nil {
			return err
		}
		found := false
		for _, f := range files {
			if strings.HasPrefix(f, version+"_") {
				files = []string{f}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("down migration for version %s not found", version)
		}
	}
	for _, name := range files {
		version := name[:4]
		if direction == "up" {
			var exists bool
			if err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
		}
		sql, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err == nil {
			if direction == "up" {
				_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version)
			} else {
				_, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, version)
			}
		}
		if err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		fmt.Println("applied", name)
	}
	return nil
}
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }

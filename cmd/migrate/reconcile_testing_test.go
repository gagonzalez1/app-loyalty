package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"clientesFrecuentes/internal/repository"
	"clientesFrecuentes/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reconciliationFixture(t *testing.T, through string, legacy bool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err = admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS citext`); err != nil {
		t.Fatal(err)
	}
	schema := "reconcile_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `CREATE TABLE schema_migrations(version CHAR(4) PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		name := filepath.Base(file)
		if name[:4] > through {
			continue
		}
		source := file
		if legacy && name[:4] == "0045" {
			source = "testdata/0045_apple_browser_identity.sql"
		}
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, name[:4]); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func fixtureState(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var result string
	err := p.QueryRow(t.Context(), `SELECT jsonb_build_object(
  'history',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM schema_migrations m),
  'flows',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM apple_browser_flows f),
  'credentials',(SELECT jsonb_agg(to_jsonb(c) ORDER BY usuario_id,client_id) FROM apple_browser_credentials c),
  'identities',(SELECT jsonb_agg(to_jsonb(i) ORDER BY usuario_id) FROM apple_identities i),
  'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM usuarios u),
  'flow_oid','apple_browser_flows'::regclass::oid,
  'credential_oid','apple_browser_credentials'::regclass::oid
 )::text`).Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func seedReconciliationData(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	_, err := p.Exec(t.Context(), `WITH u AS (
  INSERT INTO usuarios(nombre,email,tipo_cuenta) VALUES('Fixture','fixture@example.test','PERSONAL_MARCA') RETURNING id,email
 ) INSERT INTO apple_identities(usuario_id,subject,email,client_id,token_type,token_ciphertext,token_nonce)
 SELECT id,'fixture-subject',email,'native.fixture','refresh_token',decode(repeat('ab',20),'hex'),decode(repeat('cd',12),'hex') FROM u;
 INSERT INTO apple_browser_credentials SELECT usuario_id,'browser.fixture','refresh_token',decode(repeat('ef',20),'hex'),decode(repeat('01',12),'hex') FROM apple_identities;
 INSERT INTO apple_browser_flows(id,platform,nonce_hash,verifier_hash,expires_at,status,proof,proof_nonce)
 VALUES('11111111-1111-4111-8111-111111111111','web',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),now()+interval '5 minutes','ready',decode(repeat('ef',20),'hex'),decode(repeat('01',12),'hex'));`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReconcileTestingApple0045PreservesDataHistoryAndOIDs(t *testing.T) {
	p := reconciliationFixture(t, "0049", true)
	seedReconciliationData(t, p)
	before := fixtureState(t, p)
	if err := run(t.Context(), p, "up", "../../migrations"); err == nil {
		t.Fatal("normal up accepted divergent history")
	}
	if got := fixtureState(t, p); got != before {
		t.Fatal("normal up changed divergent database")
	}
	if err := reconcileTestingApple0045(t.Context(), p, "testing"); err != nil {
		t.Fatal(err)
	}
	if err := repository.New(p).CheckSchema(t.Context(), "0051"); err != nil {
		t.Fatal(err)
	}
	// Verify that the audit retains every original migration timestamp.
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var preserved bool
	err = tx.QueryRow(t.Context(), `SELECT source_history=(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM schema_migrations m WHERE version<='0049')
  AND jsonb_array_length(operations)=3 AND operations->1->>'action'='adopt_existing' FROM schema_reconciliation_log WHERE id=$1`, reconciliationID).Scan(&preserved)
	_ = tx.Rollback(t.Context())
	if err != nil || !preserved {
		t.Fatal("original history/adoption not audited", err)
	}
	var beforeWithoutNew string
	// The snapshot function uses a pool; use a temporary view of original history
	// in the query instead of changing any recorded version.
	err = p.QueryRow(t.Context(), `SELECT jsonb_build_object(
  'history',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM schema_migrations m WHERE version<='0049'),
  'flows',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM apple_browser_flows f),
  'credentials',(SELECT jsonb_agg(to_jsonb(c) ORDER BY usuario_id,client_id) FROM apple_browser_credentials c),
  'identities',(SELECT jsonb_agg(to_jsonb(i) ORDER BY usuario_id) FROM apple_identities i),
  'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM usuarios u),
  'flow_oid','apple_browser_flows'::regclass::oid,'credential_oid','apple_browser_credentials'::regclass::oid)::text`).Scan(&beforeWithoutNew)
	if err != nil || beforeWithoutNew != before {
		t.Fatal("original data, timestamp or OID changed", err)
	}
	after := fixtureState(t, p)
	for range 2 {
		if err := reconcileTestingApple0045(t.Context(), p, "testing"); err != nil {
			t.Fatal("repeat", err)
		}
		if err := run(t.Context(), p, "up", "../../migrations"); err != nil {
			t.Fatal("up after bridge", err)
		}
	}
	if fixtureState(t, p) != after {
		t.Fatal("repeat changed data or migration timestamps")
	}
	for _, name := range billingTables {
		var n int
		if err := p.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{name}.Sanitize()).Scan(&n); err != nil || n != 0 {
			t.Fatal("billing table missing or populated", name, err)
		}
	}
	t.Setenv("ALLOW_MIGRATION_DOWN", "true")
	if err := run(t.Context(), p, "down", "../../migrations"); err == nil {
		t.Fatal("down discarded reconciliation provenance")
	}
}

func TestReconcileTestingApple0045RejectsUnknownStateAtomically(t *testing.T) {
	for name, ddl := range map[string]string{
		"missing_history":     `DELETE FROM schema_migrations WHERE version='0048'`,
		"unexpected_version":  `INSERT INTO schema_migrations(version) VALUES('0050')`,
		"extra_column":        `ALTER TABLE apple_browser_flows ADD COLUMN unknown text`,
		"missing_foreign_key": `ALTER TABLE apple_browser_credentials DROP CONSTRAINT apple_browser_credentials_usuario_id_fkey`,
		"weaker_check":        `ALTER TABLE apple_browser_credentials DROP CONSTRAINT apple_browser_credentials_token_nonce_check`,
		"different_index":     `DROP INDEX apple_browser_flows_user_idx; CREATE INDEX apple_browser_flows_user_idx ON apple_browser_flows(usuario_id DESC) WHERE usuario_id IS NOT NULL`,
		"row_security":        `ALTER TABLE apple_browser_credentials ENABLE ROW LEVEL SECURITY`,
		"partial_billing":     `CREATE TABLE apple_purchase_intents(id uuid)`,
		// This collision occurs after several billing CREATE statements, proving
		// that errors roll back partial DDL as well as version/audit writes.
		"late_ddl_failure": `CREATE INDEX apple_transactions_chain_idx ON usuarios(nombre)`,
	} {
		t.Run(name, func(t *testing.T) {
			p := reconciliationFixture(t, "0049", true)
			seedReconciliationData(t, p)
			if _, err := p.Exec(t.Context(), ddl); err != nil {
				t.Fatal(err)
			}
			before := fixtureState(t, p)
			if err := reconcileTestingApple0045(t.Context(), p, "testing"); err == nil {
				t.Fatal("unknown state accepted")
			}
			if fixtureState(t, p) != before {
				t.Fatal("failed reconciliation changed rows or history")
			}
			var leaked bool
			if err := p.QueryRow(t.Context(), `SELECT to_regclass(format('%I.schema_reconciliation_log',current_schema())) IS NOT NULL
				OR to_regclass(format('%I.apple_billing_accounts',current_schema())) IS NOT NULL
				OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='puntazo_apple_0045_reference')`).Scan(&leaked); err != nil || leaked {
				t.Fatal("failed reconciliation left DDL", err)
			}
		})
	}
}

func TestReconcileTestingApple0045EnvironmentAndCanonicalUpgrade(t *testing.T) {
	for _, env := range []string{"", "production", "development"} {
		if err := reconcileTestingApple0045(t.Context(), nil, env); err == nil {
			t.Fatalf("environment %q accepted", env)
		}
	}
	p := reconciliationFixture(t, "0044", false)
	if err := run(t.Context(), p, "up", "../../migrations"); err != nil {
		t.Fatal(err)
	}
	if err := repository.New(p).CheckSchema(t.Context(), "0051"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM schema_reconciliation_log`).Scan(&n); err != nil || n != 0 {
		t.Fatal("normal upgrade fabricated reconciliation", err)
	}
	if err := reconcileTestingApple0045(t.Context(), p, "testing"); err == nil {
		t.Fatal("canonical database mistaken for divergent testing")
	}
}

func TestReconcileTestingApple0045ConcurrentAndTamperedAudit(t *testing.T) {
	p := reconciliationFixture(t, "0049", true)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- reconcileTestingApple0045(t.Context(), p, "testing") })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM schema_reconciliation_log`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate audit", err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE schema_reconciliation_log SET operations='[]'`); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTestingApple0045(t.Context(), p, "testing"); err == nil {
		t.Fatal("tampered audit accepted")
	}
}

func TestReconcileTestingApple0045EmptyDatabaseUpgrade(t *testing.T) {
	p := reconciliationFixture(t, "0000", false)
	if err := run(t.Context(), p, "up", "../../migrations"); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), p, "up", "../../migrations"); err != nil {
		t.Fatal(err)
	}
	if err := repository.New(p).CheckSchema(t.Context(), "0051"); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.ReadSource("0051_schema_reconciliation_log.up.sql"); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"clientesFrecuentes/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const reconciliationID = "testing-apple-browser-0045-to-0051"

var billingTables = []string{
	"apple_billing_accounts", "apple_purchase_intents", "apple_subscription_chains",
	"apple_transactions", "apple_renewal_state", "apple_notification_inbox",
	"apple_referral_offers", "apple_referral_charges", "apple_claim_reservations",
}

type reconciliationOperation struct {
	Action string `json:"action"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// This opt-in bridge preserves 0001-0049, including the original 0045 timestamp.
// It applies missing billing DDL and explicitly adopts equivalent browser DDL;
// it never rewrites 0045 to pretend billing was applied there originally.
func reconcileTestingApple0045(ctx context.Context, pool *pgxpool.Pool, appEnv string) error {
	if appEnv != "testing" {
		return fmt.Errorf("Apple 0045 reconciliation requires APP_ENV=testing")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='90s'; SELECT pg_advisory_xact_lock(73194211)`); err != nil {
		return err
	}
	var schema string
	if err = tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE schema_migrations IN EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT version::text FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	before, err := migrations.VersionsThrough("0049")
	if err != nil {
		return err
	}
	after, err := migrations.VersionsThrough("0051")
	if err != nil {
		return err
	}
	completed := slices.Equal(versions, after)
	if !completed && !slices.Equal(versions, before) {
		return fmt.Errorf("reconciliation supports the complete testing 0049 history or its audited 0051 result only")
	}
	var history string
	if err = tx.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(m) ORDER BY version)::text FROM schema_migrations m WHERE version<='0049'`).Scan(&history); err != nil {
		return err
	}
	names := []string{"0045_apple_billing.up.sql", "0050_apple_browser_identity.up.sql", "0051_schema_reconciliation_log.up.sql"}
	bodies := make([][]byte, len(names))
	operations := make([]reconciliationOperation, len(names))
	for i, name := range names {
		bodies[i], err = migrations.ReadSource(name)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(bodies[i])
		action := "apply"
		if i == 1 {
			action = "adopt_existing"
		}
		operations[i] = reconciliationOperation{Action: action, File: name, SHA256: hex.EncodeToString(hash[:])}
	}
	encoded, err := json.Marshal(operations)
	if err != nil {
		return err
	}
	if completed {
		var valid bool
		err = tx.QueryRow(ctx, `SELECT source_lineage='testing-apple-browser-0045' AND target_version='0051'
   AND source_history=$1::jsonb AND operations=$2::jsonb
   FROM schema_reconciliation_log WHERE id=$3`, history, string(encoded), reconciliationID).Scan(&valid)
		if err != nil || !valid {
			return fmt.Errorf("0051 history does not have matching reconciliation provenance")
		}
	}
	for _, name := range billingTables {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{schema, name}.Sanitize()).Scan(&exists); err != nil {
			return err
		}
		if exists != completed {
			return fmt.Errorf("unexpected billing object %s; reconcile its provenance separately", name)
		}
	}
	// The lock keeps definitions stable until commit. No browser row, credential
	// ciphertext, identity, or table OID is copied, replaced or logged.
	if _, err = tx.Exec(ctx, `LOCK TABLE apple_browser_flows, apple_browser_credentials IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	const reference = "puntazo_apple_0045_reference"
	if _, err = tx.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{reference}.Sanitize()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL search_path TO `+pgx.Identifier{reference}.Sanitize()+`, `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return err
	}
	// 0050's guard needs a billing marker. Its FKs resolve to the real usuarios
	// and apple_identities tables in the target schema.
	if _, err = tx.Exec(ctx, `CREATE TABLE apple_billing_accounts ()`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, string(bodies[1])); err != nil {
		return fmt.Errorf("build canonical browser reference: %w", err)
	}
	for _, name := range []string{"apple_browser_flows", "apple_browser_credentials"} {
		actual, e := tableDefinition(ctx, tx, schema, name)
		if e != nil {
			return e
		}
		wanted, e := tableDefinition(ctx, tx, reference, name)
		if e != nil {
			return e
		}
		if actual != wanted {
			return fmt.Errorf("%s differs from canonical 0050; refusing adoption", name)
		}
	}
	if _, err = tx.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{reference}.Sanitize()+` CASCADE; SET LOCAL search_path TO `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return err
	}
	if completed {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, string(bodies[0])); err != nil {
		return fmt.Errorf("apply missing Apple billing DDL: %w", err)
	}
	if _, err = tx.Exec(ctx, string(bodies[2])); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_reconciliation_log(id,source_lineage,target_version,source_history,operations)
  VALUES($1,'testing-apple-browser-0045','0051',$2::jsonb,$3::jsonb)`, reconciliationID, history, string(encoded)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0050'),('0051')`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Compare definitions generated by PostgreSQL itself, including checks, FKs,
// index expressions/options, defaults, RLS and user triggers. Row contents and
// ownership are absent; existing data is never copied or logged.
func tableDefinition(ctx context.Context, tx pgx.Tx, schema, name string) (string, error) {
	var definition string
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
  'kind',c.relkind,'persistence',c.relpersistence,'rls',c.relrowsecurity,'force_rls',c.relforcerowsecurity,
  'options',c.reloptions,'replica_identity',c.relreplident,
  'columns',(SELECT jsonb_agg(jsonb_build_array(a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,
   pg_get_expr(d.adbin,d.adrelid),a.attidentity,a.attgenerated,a.attcollation) ORDER BY a.attnum)
   FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
   WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped),
  'constraints',(SELECT jsonb_agg(jsonb_build_array(conname,contype,pg_get_constraintdef(oid),convalidated,
   condeferrable,condeferred) ORDER BY conname) FROM pg_constraint WHERE conrelid=c.oid),
  'indexes',(SELECT jsonb_agg(jsonb_build_array(ic.relname,am.amname,i.indisunique,i.indisprimary,
   i.indisvalid,i.indisready,i.indnullsnotdistinct,i.indnkeyatts,
   (SELECT jsonb_agg(pg_get_indexdef(i.indexrelid,k,false) ORDER BY k) FROM generate_series(1,i.indnatts) k),
   i.indcollation::text,i.indclass::text,i.indoption::text,pg_get_expr(i.indpred,i.indrelid)) ORDER BY ic.relname)
   FROM pg_index i JOIN pg_class ic ON ic.oid=i.indexrelid JOIN pg_am am ON am.oid=ic.relam WHERE i.indrelid=c.oid),
  'triggers',(SELECT count(*) FROM pg_trigger WHERE tgrelid=c.oid AND NOT tgisinternal),
  'policies',(SELECT count(*) FROM pg_policy WHERE polrelid=c.oid),
  'rules',(SELECT count(*) FROM pg_rewrite WHERE ev_class=c.oid),
  'inheritance',(SELECT count(*) FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
 )::text FROM pg_class c WHERE c.oid=$1::regclass`, pgx.Identifier{schema, name}.Sanitize()).Scan(&definition)
	return definition, err
}

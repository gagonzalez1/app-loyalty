package repository_test

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

// Historical migration tests deliberately exercise pre-Apple schemas.
// Only their empty Apple tables are rolled back; operational rollback gates stay intact.
func rollbackAppleMigrationFixture(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	for _, name := range []string{"0045_apple_billing", "0044_apple_identity"} {
		body, err := os.ReadFile("../../migrations/" + name + ".down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
		if _, err = p.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version=$1`, name[:4]); err != nil {
			t.Fatal(err)
		}
	}
}

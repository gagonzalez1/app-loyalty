package repository_test

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

// Historical migration tests deliberately exercise pre-Apple schemas.
// Only fixture migrations are rolled back; operational rollback gates stay intact.
func rollbackAppleMigrationFixture(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	for _, name := range []string{"0051_schema_reconciliation_log", "0050_apple_browser_identity", "0049_google_browser_flows", "0048_branch_menu_languages", "0047_branch_pdf_no_page_cap", "0046_branch_public_profile", "0045_apple_billing", "0044_apple_identity"} {
		var applied bool
		if err := p.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name[:4]).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if !applied {
			continue
		}
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

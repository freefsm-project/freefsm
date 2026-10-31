package handlers

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only empty new technical structures are removed to reconstruct the installed
// predecessor. All native approvals, invoices, identities and grants survive.
func removeEmptyCoverageSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	removeEmptyNoticeSchema(t, pool)
	ctx := context.Background()
	var empty bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_coverage_events) AND NOT EXISTS(SELECT 1 FROM lease_coverage_revisions) AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations WHERE kind='coverage')`).Scan(&empty))
	historyEqual(t, "old fixture has no coverage events", empty, true)
	_, err := pool.Exec(ctx, `DROP FUNCTION lease_event_coverage_validate() CASCADE; DROP VIEW lease_coverage_current; DROP TABLE lease_coverage_revisions,lease_coverage_events; ALTER TABLE lease_billing_obligations DROP CONSTRAINT lease_billing_obligations_kind_check; ALTER TABLE lease_billing_obligations ADD CHECK(kind IN ('initial','monthly')); ALTER TABLE lease_billing_period_failures DROP CONSTRAINT lease_billing_period_failures_kind_check; ALTER TABLE lease_billing_period_failures ADD CHECK(kind IN ('initial','monthly')); ALTER TABLE lease_billing_period_dispositions DROP CONSTRAINT lease_billing_period_dispositions_kind_check; ALTER TABLE lease_billing_period_dispositions ADD CHECK(kind IN ('initial','monthly')); DELETE FROM schema_migrations WHERE name='120_lease_coverage_events'`)
	historyCheck(t, err)
}

func TestLeaseCoveragePopulatedUpgrade(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	removeEmptyCoverageSchema(t, pool)
	tables := []string{"leases", "lease_finalizations", "lease_intended_assets", "lease_draft_asset_terms", "lease_billing_obligations", "lease_invoice_generations", "lease_asset_dependencies", "lease_master_dependencies", "invoices", "issued_document_numbers", "company_settings", "authorization_audit", "authorization_roles", "authorization_grants", "customers", "assets", "locations"}
	before := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "coverage upgrade preserves all populated old roots", historyDurableRows(t, pool, tables), before)
	var empty bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_coverage_events) AND NOT EXISTS(SELECT 1 FROM lease_coverage_revisions)`).Scan(&empty))
	historyEqual(t, "no invented coverage history", empty, true)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "coverage migration replay", historyDurableRows(t, pool, tables), before)
}

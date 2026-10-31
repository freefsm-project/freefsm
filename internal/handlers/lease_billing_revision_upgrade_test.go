package handlers

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/database"
)

func TestLeaseBillingRevisionPopulatedUpgrade(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	tables := []string{"leases", "lease_finalizations", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_obligations", "lease_invoice_generations", "lease_coverage_events", "lease_coverage_revisions", "lease_notice_revisions", "lease_fact_corrections", "invoices", "assets", "locations", "customers", "authorization_roles", "authorization_grants", "authorization_audit", "issued_document_numbers"}
	before := leasePreservationRows(t, pool, tables)
	_, err := pool.Exec(ctx, `DROP TABLE lease_billing_revisions; DELETE FROM schema_migrations WHERE name='123_lease_billing_revisions'`)
	historyCheck(t, err)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "upgrade preserves all recorded facts identifiers and grants", leasePreservationRows(t, pool, tables), before)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_revisions`).Scan(&count))
	historyEqual(t, "no invented history", count, 0)
}

package handlers

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"io/fs"
	"regexp"
	"testing"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reconstruct the exact predecessor functions, removing only empty new stores.
// Installed business records and prior immutable histories remain untouched.
func removeEmptyNoticeSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var empty bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_billing_revisions)`).Scan(&empty))
	historyEqual(t, "no billing revision history in predecessor fixture", empty, true)
	_, billingErr := pool.Exec(ctx, `DROP TABLE lease_billing_revisions; DELETE FROM schema_migrations WHERE name='123_lease_billing_revisions'`)
	historyCheck(t, billingErr)
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_fact_corrections)`).Scan(&empty))
	historyEqual(t, "no correction history in predecessor fixture", empty, true)
	_, err := pool.Exec(ctx, `DROP TABLE lease_fact_corrections; DELETE FROM schema_migrations WHERE name='122_lease_fact_corrections'`)
	historyCheck(t, err)
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_notice_revisions)`).Scan(&empty))
	historyEqual(t, "no notice history in predecessor fixture", empty, true)
	for _, definition := range []struct{ file, name string }{
		{"103_master_lease_dependencies.up.sql", "lease_customer_blocks_archive"},
		{"103_master_lease_dependencies.up.sql", "lease_premises_blocks_archive"},
		{"103_master_lease_dependencies.up.sql", "lease_asset_blocks_archive"},
		{"106_draft_lease_details.up.sql", "lease_registration_dependency_check"},
		{"111_lease_finalized_selection_guard.up.sql", "lease_finalized_draft_guard"},
		{"120_lease_coverage_events.up.sql", "lease_event_coverage_validate"},
	} {
		raw, err := fs.ReadFile(database.MigrationFS(), definition.file)
		historyCheck(t, err)
		pattern := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION ` + definition.name + `\(.*?\$\$;`)
		sql := pattern.FindString(string(raw))
		if sql == "" {
			t.Fatalf("missing predecessor function %s", definition.name)
		}
		sql = regexp.MustCompile(`^CREATE (?:OR REPLACE )?FUNCTION`).ReplaceAllString(sql, "CREATE OR REPLACE FUNCTION")
		_, err = pool.Exec(ctx, sql)
		historyCheck(t, err)
	}
	_, err = pool.Exec(ctx, `DROP VIEW lease_notice_current; DROP TABLE lease_notice_revisions; DROP FUNCTION lease_has_ended(bigint); DELETE FROM schema_migrations WHERE name='121_lease_notice_history'`)
	historyCheck(t, err)
}

func TestLeaseNoticePopulatedUpgrade(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	var outgoing, typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT a.id,a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, lease).Scan(&outgoing, &typ))
	incoming := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Installed replacement").SaveX(ctx)
	_, err = authorization.New(pool).ChangeLeaseCoverage(ctx, authorization.Actor{UserID: admin}, lease, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: "2024-02-10", OutgoingID: outgoing, IncomingID: incoming.ID}, Action: "create", Reason: "Installed approved replacement", OperationKey: "notice-upgrade-coverage-0001"})
	historyCheck(t, err)
	removeEmptyNoticeSchema(t, pool)
	tables := []string{"leases", "lease_finalizations", "lease_coverage_events", "lease_coverage_revisions", "lease_billing_obligations", "lease_invoice_generations", "lease_asset_dependencies", "lease_master_dependencies", "invoices", "issued_document_numbers", "company_settings", "authorization_audit", "authorization_roles", "authorization_grants", "customers", "assets", "locations"}
	before := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "notice upgrade preserves all installed roots", historyDurableRows(t, pool, tables), before)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_notice_revisions`).Scan(&count))
	historyEqual(t, "no invented notice history", count, 0)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "migration replay", historyDurableRows(t, pool, tables), before)
}

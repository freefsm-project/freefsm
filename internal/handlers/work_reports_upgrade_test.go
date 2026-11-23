package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
)

// Start at an actual earlier migration boundary, populate retained commercial
// history there, then apply the production forward migrations. Report registration
// must not manufacture historical stages or rewrite an existing CSV artifact.
func TestWorkReportsEarlierSchemaUpgradePreservesCohortFactsHTTP(t *testing.T) {
	prior := onboardingMigrationsThrough(t, "124_lease_manual_adjustments.up.sql")
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var upgraded bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='125_service_entry_batch_receipts')`).Scan(&upgraded))
	if upgraded {
		t.Fatal("fixture did not start on genuine earlier schema")
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed cohort Customer").SaveX(ctx)
	fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Installed cohort source"}, "line_items": {"[]"}}, 303)
	var estimate int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates WHERE title='Installed cohort source'`).Scan(&estimate))
	path := fmt.Sprintf("/estimates/%d/convert", estimate)
	fieldPost(t, web, cookie, path, conversionReview(t, web, cookie, path), 303)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, estimate).Scan(&invoice))
	path = fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice)
	fieldPost(t, web, cookie, path, conversionReview(t, web, cookie, path), 303)
	path = fmt.Sprintf("/estimates/%d/convert", estimate)
	fieldPost(t, web, cookie, path, conversionReview(t, web, cookie, path), 303)
	_, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Installed report reader", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.All}})
	historyCheck(t, err)
	artifact := masterPost(t, web, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	oldCSV := requestBody(t, web, cookie, "GET", artifact+"/download", 200)
	tables := []string{"estimates", "invoices", "estimate_invoice_conversion_cycles", "operation_admissions", "authorization_roles", "authorization_grants", "export_jobs", "export_records"}
	before := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='125_service_entry_batch_receipts')`).Scan(&upgraded))
	if !upgraded {
		t.Fatal("fixture did not apply forward migration")
	}
	historyEqual(t, "forward upgrade retains identities, original created_at, both cycles, grants and old artifacts", historyDurableRows(t, pool, tables), before)
	var total, active int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE reverted_at IS NULL) FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, estimate).Scan(&total, &active))
	if total != 2 || active != 1 {
		t.Fatalf("retained cycles total=%d active=%d", total, active)
	}
	if got := requestBody(t, web, cookie, "GET", artifact+"/download", 200); got != oldCSV {
		t.Fatal("registration/upgrade changed historical CSV bytes")
	}
	assertContains(t, requestBody(t, web, cookie, "GET", "/reports/estimate-cohort?scope=A", 200), "Installed cohort source")
	requestBody(t, web, cookie, "GET", "/reports/workload?scope=A", 200)
}

package handlers

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestCommercialArchiveInstalledUpgradePreservesHistoryAndGrants(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "101_invoice_archive_") || strings.HasPrefix(entry.Name(), "102_commercial_archive_") {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, _, _, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	i := archiveDraftFixture(t, pool, client, company)
	// An already-installed archived Draft is retained honestly, not silently
	// finalized, voided, deleted, or supplied with a fabricated archive actor.
	client.Invoice.UpdateOneID(i.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(i.CustomerID).SetTitle("Installed history").SetDeletedAt(time.Now()).SetValidThrough(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
	_, err = authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Installed historical viewer", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.archived.view", Scope: authorization.All}})
	historyCheck(t, err)
	tables := []string{"estimates", "invoices", "customers", "authorization_roles", "authorization_grants", "authorization_audit", "activity_logs", "document_deliveries", "document_delivery_events", "document_pdf_versions", "estimate_invoice_conversion_cycles"}
	// Empty histories are deliberate installed facts, not missing fixture work:
	// the upgrade must not fabricate any event, Delivery, PDF, or cycle.
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var rows string
			historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(d) ORDER BY to_jsonb(d)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` d`).Scan(&rows))
			out[table] = rows
		}
		return out
	}
	before := snapshot()
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "archive upgrade preserves every recorded row and grant", snapshot(), before)
	if _, err := pool.Exec(ctx, `UPDATE estimates SET notes='silent repair' WHERE id=$1`, e.ID); err == nil {
		t.Fatal("upgraded archived history was writable")
	}
	if _, err := pool.Exec(ctx, `UPDATE invoices SET deleted_at=NULL,notes='combined restore edit' WHERE id=$1`, i.ID); err == nil {
		t.Fatal("restore smuggled a content edit")
	}
	historyEqual(t, "failed upgrade-era writes preserve all history", snapshot(), before)
}

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func leasePreservationRows(t *testing.T, pool *pgxpool.Pool, tables []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range tables {
		var rows string
		historyCheck(t, pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&rows))
		out[table] = rows
	}
	return out
}

// Populate through the owning writers, so preservation checks cannot pass with
// empty coverage/generation tables or synthetic commencement snapshots.
func populateFinalizedLease(t *testing.T, pool *pgxpool.Pool, client *ent.Client, web http.Handler, cookie *http.Cookie, company int64) (int64, url.Values) {
	t.Helper()
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Finalized preservation Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Commencement premises").SetAddress1("42 Recorded street").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Finalized equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Covered equipment").SaveX(ctx)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, 0, 30).Format(time.DateOnly)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {start}, "ends_on": {today.AddDate(1, 0, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	fieldPost(t, web, cookie, path+"/billing", url.Values{"revision": {"0"}, "initial_due_on": {start}, "asset_id": {fmt.Sprint(a.ID)}, "rent_cents": {"5000"}, "initial_cents": {"101"}, "tax_rate": {"5"}}, 303)
	values := url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {start}}
	fieldPost(t, web, cookie, path+"/finalize", values, 303)
	assertFinalizedPreservation(t, pool, id)
	return id, values
}

func assertFinalizedPreservation(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	var valid bool
	historyCheck(t, pool.QueryRow(context.Background(), `SELECT
 EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=$1 AND ends_on IS NULL)
 AND EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=$1 AND premises_snapshot->>'address_1'='42 Recorded street')
 AND (SELECT count(*) FROM lease_billing_obligations WHERE lease_id=$1)=2
 AND (SELECT count(*) FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=$1 AND o.kind='initial')=1
 AND EXISTS(SELECT 1 FROM lease_billing_obligations o WHERE o.lease_id=$1 AND o.kind='monthly' AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id))
 AND EXISTS(SELECT 1 FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id JOIN invoices i ON i.id=g.invoice_id
 WHERE o.lease_id=$1 AND i.lease_id=$1 AND i.line_items::jsonb=o.line_items AND i.financial=o.financial
 AND (o.line_items->0->>'lease_id')::bigint=$1 AND o.line_items->0->>'period'=o.due_on::text
 AND o.line_items->0->>'charge_kind'=o.kind AND EXISTS(SELECT 1 FROM lease_asset_dependencies d WHERE d.lease_id=$1 AND d.asset_id=(o.line_items->0->>'asset_id')::bigint))`, id).Scan(&valid))
	if !valid {
		t.Fatal("missing populated coverage, pending identity or generated Invoice line provenance")
	}
}

func voidLeaseInvoiceAndReplay(t *testing.T, pool *pgxpool.Pool, web http.Handler, cookie *http.Cookie, company, actor, id int64, values url.Values) {
	t.Helper()
	ctx := context.Background()
	var invoice, target int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, id).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&target))
	workflow := statusflow.New(pool)
	a := statusflow.Actor{ID: actor, CompanyID: company, Role: "admin"}
	historyCheck(t, workflow.FinalizeInvoice(ctx, a, invoice))
	historyCheck(t, workflow.TransitionInvoice(ctx, a, invoice, target))
	tables := []string{"lease_finalizations", "lease_asset_dependencies", "lease_billing_obligations", "lease_invoice_generations", "invoices", "issued_document_numbers", "company_settings", "authorization_audit"}
	before := historyDurableRows(t, pool, tables)
	fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d/finalize", id), values, 303)
	historyEqual(t, "Void retains generation identity without replacement on replay", historyDurableRows(t, pool, tables), before)
	category, err := workflow.CurrentCategory(ctx, company, statusflow.Invoice, invoice)
	historyCheck(t, err)
	historyEqual(t, "generated Invoice remains Void", category, statusflow.InvoiceVoid)
}

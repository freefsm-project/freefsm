package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const leaseExportReviewPath = "/leases/report/export"

// Independent copies of the versioned public contract; never derive expected
// headers from exports.Leases.Columns, which is the implementation under test.
const leaseExportOperationalHeader = "row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,starts_on,fixed_ends_on,termination_on,first_monthly_on,renewal_starts_on,renewal_ends_on,asset_id,asset_number,asset_name,coverage_starts_on,coverage_ends_on,placement,matching_lease_count,history_status"
const leaseExportBillingHeader = "row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,asset_id,effective_on,recorded_at,actor_id,reason,monthly_rent,scheduled_monthly_rent,tax_rate,revision_id,before_rent,after_rent,before_tax_rate,after_tax_rate,period_kind,due_on,period_state,obligation_id,obligation_revision_id,invoice_id,invoice_number,generated_at,generated_total,generated_tax,tax_exempt,manual_invoice_status,effective_invoice_status,review_required,line_ordinal,line_amount,line_taxable,line_billing_revision_id,settlement_id,settlement_kind,settlement_amount,settlement_effective_on,reversed_on,adjustment_id,adjustment_kind,adjustment_amount,adjustment_starts_on,adjustment_ends_on,matching_lease_count,matching_monthly_rent,failure_history,revision_line_ordinal,history_status"

// Production BrowserCSRF validates these forms before guardedOnlineActions can
// cache ParseForm values. Direct HTTP tests retain that exact strict decoding
// order; the browser fixture additionally exercises real nosurf tokens.
func leaseExportHTTPFixture(t *testing.T) (*pgxpool.Pool, *ent.Client, http.Handler, *services.SessionService, int64, int64) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := middleware.DecodeFinancialExportForm(r); err != nil {
			http.Error(w, "Invalid export filters", http.StatusBadRequest)
			return
		}
		router.ServeHTTP(w, r)
	})
	return pool, client, web, sessions, company, admin
}

// These tests cross the registered HTTP boundary, durable production worker and
// download boundary. CSV assertions do not call the production encoder/parser.
func leaseExportPrepare(t *testing.T, pool *pgxpool.Pool, web http.Handler, cookie *http.Cookie, values url.Values) string {
	t.Helper()
	v := url.Values{}
	for key, items := range values {
		v[key] = slices.Clone(items)
	}
	if v.Get("operation_id") == "" {
		v.Set("operation_id", newOperationKey())
	}
	path := masterPost(t, web, cookie, leaseExportReviewPath, v, http.StatusSeeOther)
	_, err := exports.Product(pool).ProcessOne(context.Background())
	historyCheck(t, err)
	return path
}

func leaseExportRows(t *testing.T, body string) []map[string]string {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	historyCheck(t, err)
	if len(records) < 2 {
		t.Fatalf("missing CSV header/summary: %q", body)
	}
	header := records[0]
	seen := map[string]bool{}
	for _, name := range header {
		if name == "" || seen[name] {
			t.Fatalf("invalid duplicate/empty CSV column %q", name)
		}
		seen[name] = true
	}
	if !seen["row_type"] || !seen["lease_id"] {
		t.Fatalf("missing typed Lease contract: %v", header)
	}
	wantColumns := 27
	if seen["generated_total"] {
		wantColumns = 59
	}
	if len(header) != wantColumns {
		t.Fatalf("CSV columns=%d want %d: %v", len(header), wantColumns, header)
	}
	wantHeader := leaseExportOperationalHeader
	if seen["generated_total"] {
		wantHeader = leaseExportBillingHeader
	}
	historyEqual(t, "fixed ordered CSV header", strings.Join(header, ","), wantHeader)
	var out []map[string]string
	for _, record := range records[1:] {
		row := map[string]string{}
		for i, name := range header {
			row[name] = record[i]
		}
		out = append(out, row)
	}
	if out[len(out)-1]["row_type"] != "summary" {
		t.Fatal("summary must be published after every matching Lease")
	}
	for _, row := range out {
		for _, key := range []string{"as_of", "timezone", "snapshot_at"} {
			historyEqual(t, "one snapshot metadata "+key, row[key], out[0][key])
		}
	}
	return out
}

func leaseExportKind(rows []map[string]string, kind string) []map[string]string {
	var out []map[string]string
	for _, row := range rows {
		if row["row_type"] == kind {
			out = append(out, row)
		}
	}
	return out
}

func leaseExportGet(t *testing.T, pool *pgxpool.Pool, web http.Handler, cookie *http.Cookie, values url.Values) ([]map[string]string, string) {
	t.Helper()
	path := leaseExportPrepare(t, pool, web, cookie, values)
	return leaseExportRows(t, requestBody(t, web, cookie, "GET", path+"/download", 200)), path
}

func leaseExportClock(t *testing.T, pool *pgxpool.Pool, day string) {
	t.Helper()
	_, err := time.Parse(time.DateOnly, day)
	historyCheck(t, err)
	_, err = pool.Exec(context.Background(), `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+day+`'::date $$`)
	historyCheck(t, err)
}

// All historical timestamps are assigned on INSERT in this isolated schema.
// No immutable event is rewritten to manufacture a historical export.
func leaseExportRecordingClock(t *testing.T, pool *pgxpool.Pool, instant string) {
	t.Helper()
	_, err := time.Parse(time.RFC3339, instant)
	historyCheck(t, err)
	_, err = pool.Exec(context.Background(), `CREATE OR REPLACE FUNCTION lease_export_fixture_recorded() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.recorded_at='`+instant+`'::timestamptz; RETURN NEW; END $$;
CREATE OR REPLACE FUNCTION lease_export_fixture_created() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.created_at='`+instant+`'::timestamptz; RETURN NEW; END $$;`)
	historyCheck(t, err)
}

func leaseExportInstallRecordingClock(t *testing.T, pool *pgxpool.Pool, instant string) {
	t.Helper()
	leaseExportRecordingClock(t, pool, instant)
	for _, table := range []string{"authorization_audit", "lease_finalizations", "lease_invoice_generations", "lease_billing_source_changes", "lease_notice_revisions", "lease_billing_revisions", "lease_manual_adjustments"} {
		_, err := pool.Exec(context.Background(), `CREATE TRIGGER lease_export_fixture_recorded BEFORE INSERT ON `+pgx.Identifier{table}.Sanitize()+` FOR EACH ROW EXECUTE FUNCTION lease_export_fixture_recorded()`)
		historyCheck(t, err)
	}
	for _, table := range []string{"leases", "invoices"} {
		_, err := pool.Exec(context.Background(), `CREATE TRIGGER lease_export_fixture_created BEFORE INSERT ON `+pgx.Identifier{table}.Sanitize()+` FOR EACH ROW EXECUTE FUNCTION lease_export_fixture_created()`)
		historyCheck(t, err)
	}
}

func leaseExportAgreement(t *testing.T, pool *pgxpool.Pool, client *ent.Client, company, admin int64, title, start, end string, rents ...int64) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName(title + " Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle(title + " premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName(title + " equipment").SaveX(ctx)
	var assets []int64
	for i := range rents {
		a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName(fmt.Sprintf("%s Asset %d", title, i)).SaveX(ctx)
		assets = append(assets, a.ID)
	}
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: title, StartsOn: start, EndsOn: end, PremisesID: &l.ID, AssetIDs: assets}))
	zero := int64(0)
	for i, rent := range rents {
		historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, int64(i), start, authorization.DraftLeaseTerms{AssetID: assets[i], InitialCents: &zero, RentCents: &rent, TaxRate: "5"}, 0))
	}
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: int64(len(rents)), ManagedBillingStart: start}))
	return lease.ID, assets
}

func leaseExportGrants(keys ...string) []authorization.Grant {
	var out []authorization.Grant
	for _, key := range keys {
		out = append(out, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	return out
}

func TestLeaseExportAllMatchingAndPreservationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	svc := authorization.New(pool)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("All matching Customer").SaveX(ctx)
	var expected []string
	for i := 0; i < 104; i++ {
		lease, err := svc.RegisterDraftLease(ctx, authorization.Actor{UserID: admin}, customer.ID)
		historyCheck(t, err)
		title := fmt.Sprintf("Export cohort %03d, 雪 \"quoted\"\nline", i)
		if i == 103 {
			title = "Excluded sentinel"
		} else {
			expected = append(expected, fmt.Sprint(lease.ID))
		}
		historyCheck(t, svc.SaveDraftLeaseDetails(ctx, authorization.Actor{UserID: admin}, lease.ID, authorization.DraftLeaseDetailsInput{Title: title}))
	}
	tables := []string{"leases", "customers", "assets", "locations", "issued_document_numbers", "authorization_roles", "authorization_grants", "lease_finalizations", "lease_notice_revisions", "lease_coverage_revisions", "lease_billing_revisions", "lease_invoice_generations", "invoices"}
	before := leasePreservationRows(t, pool, tables)
	for _, sort := range []string{"number", "number_desc"} {
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "search": {"Export cohort"}, "sort": {sort}})
		leases := leaseExportKind(rows, "lease")
		if len(leases) != 103 {
			t.Fatalf("all matching exported %d unique Lease rows, want 103", len(leases))
		}
		want := slices.Clone(expected)
		if sort == "number_desc" {
			slices.Reverse(want)
		}
		for i, row := range leases {
			historyEqual(t, "Number order", row["lease_id"], want[i])
		}
		if len(leaseExportKind(rows, "summary")) != 1 {
			t.Fatal("one matching-set summary required")
		}
		historyEqual(t, "distinct matching count", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "103")
	}
	historyEqual(t, "export preserves installed facts and grants", leasePreservationRows(t, pool, tables), before)
}

func TestLeaseExportJuneGeneratedDraftHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-06-01")
	leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
	lease, _ := leaseExportAgreement(t, pool, client, company, admin, "June immutable July Draft", "2024-01-01", "2024-12-31", 3200, 1800)
	var invoice int64
	var original string
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.id,jsonb_build_object('invoice',to_jsonb(i),'generation',to_jsonb(g),'financial',coalesce(r.financial,o.financial),'lines',coalesce(r.line_items,o.line_items))::text FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id LEFT JOIN lease_billing_obligation_revisions r ON r.id=g.obligation_revision_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-07-01'`, lease).Scan(&invoice, &original))
	leaseExportClock(t, pool, "2024-06-10")
	leaseExportRecordingClock(t, pool, "2024-06-10T12:00:00Z")
	historyCheck(t, authorization.New(pool).ChangeLeaseNotice(ctx, authorization.Actor{UserID: admin}, lease, authorization.LeaseNoticeInput{Action: "create", Category: "customer_early", Initiator: "customer", NoticeOn: "2024-06-10", EffectiveOn: "2024-06-30", Reason: "Accepted June decision", OperationKey: newOperationKey()}))
	leaseExportClock(t, pool, "2024-08-01")
	for _, day := range []string{"2024-06-01", "2024-06-09", "2024-06-10", "2024-06-30", "2024-07-01", "2024-08-01"} {
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "as_of": {day}, "search": {"June immutable"}})
		var found map[string]string
		for _, row := range leaseExportKind(rows, "invoice") {
			if row["invoice_id"] == fmt.Sprint(invoice) {
				found = row
			}
		}
		if found == nil {
			t.Fatalf("%s omitted July Draft generated June 1", day)
		}
		historyEqual(t, "captured July amount", found["generated_total"], "52.50")
		historyEqual(t, "July remains original manual Draft", found["manual_invoice_status"], "invoice:draft")
		historyEqual(t, "review recording cutoff", found["review_required"], strconv.FormatBool(day >= "2024-06-10"))
		leases := leaseExportKind(rows, "lease")
		if len(leases) != 1 {
			t.Fatalf("June Lease rows: %v", leases)
		}
		want := "active"
		if day > "2024-06-30" {
			want = "terminated"
		}
		historyEqual(t, "inclusive lifecycle", leases[0]["lifecycle"], want)
		for _, row := range leaseExportKind(rows, "period") {
			if row["due_on"] > "2024-06-30" && (row["period_state"] == "pending" || row["period_state"] == "ungenerated") {
				t.Fatalf("unowed period in pending queue: %v", row)
			}
		}
	}
	var after string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('invoice',to_jsonb(i),'generation',to_jsonb(g),'financial',coalesce(r.financial,o.financial),'lines',coalesce(r.line_items,o.line_items))::text FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id LEFT JOIN lease_billing_obligation_revisions r ON r.id=g.obligation_revision_id WHERE i.id=$1`, invoice).Scan(&after))
	historyEqual(t, "June notice and exports never rewrite July generation", after, original)
}

func leaseExportReader(t *testing.T, pool *pgxpool.Pool, client *ent.Client, company, admin int64, name string, grants []authorization.Grant) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, name, grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail(newOperationKey() + "@lease-export.test").SetName(name).SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	return u.ID, role
}

func TestLeaseExportIndependentGrantsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	t.Run("export without compatible base view", func(t *testing.T) {
		user, role := leaseExportReader(t, pool, client, company, admin, "Lost base View", leaseExportGrants("lease.view", "lease.export"))
		_, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.view'`, role)
		historyCheck(t, err)
		cookie := sessionCookie(t, ctx, sessions, user)
		masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"mode": {"operational"}, "operation_id": {newOperationKey()}}, 403)
	})
	for _, test := range []struct {
		name, mode string
		grants     []string
		status     int
	}{
		{"view only", "operational", []string{"lease.view"}, 403},
		{"billing without export", "billing", []string{"lease.view", "lease.billing.view"}, 403},
		{"base exporter", "operational", []string{"lease.view", "lease.export"}, 303},
		{"billing forged", "billing", []string{"lease.view", "lease.export"}, 403},
		{"billing independent", "billing", []string{"lease.view", "lease.export", "lease.billing.view"}, 303},
	} {
		t.Run(test.name, func(t *testing.T) {
			user, _ := leaseExportReader(t, pool, client, company, admin, test.name, leaseExportGrants(test.grants...))
			cookie := sessionCookie(t, ctx, sessions, user)
			op := newOperationKey()
			path := masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"operation_id": {op}, "mode": {test.mode}}, test.status)
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs WHERE operation_id=$1`, op).Scan(&count))
			if test.status == 403 {
				historyEqual(t, "denied acceptance has no job", count, 0)
				return
			}
			historyEqual(t, "one accepted job", count, 1)
			_, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			rows := leaseExportRows(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
			historyEqual(t, "empty summary count", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "0")
			expectStatus(t, web, sessionCookie(t, ctx, sessions, admin), "GET", path+"/download", 404)
		})
	}
}

func TestLeaseExportOperationalPrivileged(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-06-01")
	lease, assets := leaseExportAgreement(t, pool, client, company, admin, "Operational independent", "2024-01-01", "2024-12-31", 5000)
	before, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
	historyCheck(t, authorization.New(pool).ReviseLeaseBilling(ctx, authorization.Actor{UserID: admin}, lease, authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-06-01", Reason: "Hidden financial change", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: assets[0], RentCents: 9900, TaxRate: "7"}}}))
	after, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
	for _, rows := range [][]map[string]string{before, after} {
		for _, row := range rows {
			delete(row, "snapshot_at")
			if _, exists := row["monthly_rent"]; exists {
				t.Fatal("financial column in operational contract")
			}
		}
	}
	historyEqual(t, "hidden financial edits cannot change operational selection", after, before)
	// Rename only a financial history table in this disposable schema. The
	// operational export must not query it, even for the protected Administrator.
	_, err := pool.Exec(ctx, `ALTER TABLE lease_billing_revisions RENAME TO lease_export_inaccessible_billing_revisions`)
	historyCheck(t, err)
	defer func() {
		_, err := pool.Exec(ctx, `ALTER TABLE lease_export_inaccessible_billing_revisions RENAME TO lease_billing_revisions`)
		historyCheck(t, err)
	}()
	leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
}

func TestLeaseExportGrantGainPreservesBytes(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	grants := leaseExportGrants("lease.view", "lease.export")
	user, role := leaseExportReader(t, pool, client, company, admin, "Gain independent facets", grants)
	cookie := sessionCookie(t, ctx, sessions, user)
	path := leaseExportPrepare(t, pool, web, cookie, url.Values{"mode": {"operational"}})
	before := requestBody(t, web, cookie, "GET", path+"/download", 200)
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	grants = append(grants, leaseExportGrants("lease.billing.view", "customer.view", "asset.view", "location.view", "invoice.view", "pricing.view")...)
	_, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Gain independent facets", grants, revision)
	historyCheck(t, err)
	historyEqual(t, "authority gain never enriches prepared bytes", requestBody(t, web, cookie, "GET", path+"/download", 200), before)
	// Losing an unused financial facet cannot invalidate operational bytes.
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.billing.view'`, role)
	historyCheck(t, err)
	historyEqual(t, "unused facet loss", requestBody(t, web, cookie, "GET", path+"/download", 200), before)
}

func TestLeaseExportEmptySensitiveFiltersAndSummaryReferences(t *testing.T) {
	for _, facet := range []string{"customer", "asset", "premises", "complete_generation", "summary_asset"} {
		t.Run(facet, func(t *testing.T) {
			pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
			ctx := context.Background()
			lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
			var customer, premises, asset int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT l.customer_id,l.premises_id,d.asset_id FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id WHERE l.id=$1`, lease).Scan(&customer, &premises, &asset))
			grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view", "customer.view", "location.view", "asset.view", "invoice.view", "invoice.archived.view", "pricing.view")
			user, role := leaseExportReader(t, pool, client, company, admin, "Sensitive selection", grants)
			cookie := sessionCookie(t, ctx, sessions, user)
			values := url.Values{"mode": {"operational"}, "search": {"NO MATCH PRIVATE SELECTION"}}
			permission := ""
			switch facet {
			case "customer":
				values.Set("customer", fmt.Sprint(customer))
				permission = "customer.view"
			case "asset":
				values.Set("asset", fmt.Sprint(asset))
				permission = "asset.view"
			case "premises":
				values.Set("premises", fmt.Sprint(premises))
				permission = "location.view"
			case "complete_generation":
				values.Set("mode", "billing")
				values.Set("billing", "ungenerated")
				permission = "invoice.archived.view"
			case "summary_asset":
				values.Del("search")
				permission = "asset.view"
				var start string
				historyCheck(t, pool.QueryRow(ctx, `SELECT starts_on::text FROM leases WHERE id=$1`, lease).Scan(&start))
				leaseExportClock(t, pool, start)
			}
			rows, path := leaseExportGet(t, pool, web, cookie, values)
			want := "0"
			if facet == "summary_asset" {
				want = "1"
			}
			historyEqual(t, "summary fixture cardinality", leaseExportKind(rows, "summary")[0]["matching_lease_count"], want)
			_, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, permission)
			historyCheck(t, err)
			denied := requestBody(t, web, cookie, "GET", path+"/download", 410)
			assertNotContains(t, denied, "NO MATCH PRIVATE SELECTION")
			assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), "NO MATCH PRIVATE SELECTION")
		})
	}
}

func TestLeaseExportHistoricalFailureRetryUnavailable(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-01-01")
	leaseExportInstallRecordingClock(t, pool, "2024-01-01T12:00:00Z")
	lease, _ := leaseExportAgreement(t, pool, client, company, admin, "Failure history unavailable", "2024-01-01", "2024-12-31", 5000)
	leaseExportClock(t, pool, "2024-01-02")
	leaseExportRecordingClock(t, pool, "2024-01-02T12:00:00Z")
	_, err := pool.Exec(ctx, `CREATE FUNCTION lease_export_fail_invoice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'lease export fixture generation failure'; END $$; CREATE TRIGGER lease_export_fail_invoice BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION lease_export_fail_invoice()`)
	historyCheck(t, err)
	if err := authorization.New(pool).GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("expected owning generation failure")
	}
	var failures int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_period_failures WHERE lease_id=$1`, lease).Scan(&failures))
	if failures == 0 {
		t.Fatal("fixture did not retain real failure")
	}
	rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "billing": {"failed"}})
	historyEqual(t, "current failed selection", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "1")
	_, err = pool.Exec(ctx, `DROP TRIGGER lease_export_fail_invoice ON invoices`)
	historyCheck(t, err)
	leaseExportClock(t, pool, "2024-01-03")
	leaseExportRecordingClock(t, pool, "2024-01-03T12:00:00Z")
	historyCheck(t, authorization.New(pool).GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_period_failures WHERE lease_id=$1`, lease).Scan(&failures))
	historyEqual(t, "successful retry clears current failure", failures, 0)
	rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "as_of": {"2024-01-02"}})
	historyEqual(t, "past worker status is unavailable", leaseExportKind(rows, "lease")[0]["failure_history"], "unavailable")
	for _, row := range leaseExportKind(rows, "period") {
		if row["period_state"] == "pending" || row["period_state"] == "failed" {
			t.Fatalf("invented past worker status: %v", row)
		}
	}
	for _, filter := range []string{"failed", "pending"} {
		expectStatus(t, web, cookie, "GET", leaseExportReviewPath+"?mode=billing&as_of=2024-01-02&billing="+filter, 422)
	}
}

func TestLeaseExportOriginalAndCurrentAssignedAnchors(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	var invoice, customer int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,customer_id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice, &customer))
	a := client.Job.Create().SetCompanyID(company).SetCustomerID(customer).SetJobType("Original Invoice scope anchor").SaveX(ctx)
	b := client.Job.Create().SetCompanyID(company).SetCustomerID(customer).SetJobType("Current Invoice scope anchor").SaveX(ctx)
	grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view")
	for _, permission := range []string{"job.view", "invoice.view", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.Assigned})
	}
	grants = append(grants, leaseExportGrants("customer.view", "customer.financial.view", "settlement.view")...)
	user, _ := leaseExportReader(t, pool, client, company, admin, "Original Assigned anchors", grants)
	cookie := sessionCookie(t, ctx, sessions, user)
	_, err := pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2;`, a.ID, invoice)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, a.ID, user)
	historyCheck(t, err)
	rows, path := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	if len(leaseExportKind(rows, "invoice")) != 1 {
		t.Fatal("Assigned fixture did not disclose generated Invoice")
	}
	before := requestBody(t, web, cookie, "GET", path+"/download", 200)
	// A denied download permanently blocks its receipt; use a second identical
	// original-anchor artifact for the later both-anchors-authorized assertion.
	probe := leaseExportPrepare(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	_, err = pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, b.ID, invoice)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "GET", probe+"/download", 410)
	_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, b.ID, user)
	historyCheck(t, err)
	historyEqual(t, "both anchors retain immutable bytes", requestBody(t, web, cookie, "GET", path+"/download", 200), before)
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, a.ID, user)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "GET", path+"/download", 410)
	fresh, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	if len(leaseExportKind(fresh, "invoice")) != 1 {
		t.Fatal("current-anchor export must remain independently authorized")
	}
}

func TestLeaseExportAgreementPositionCoherence(t *testing.T) {
	t.Run("corrected commencement and scheduled cancellation", func(t *testing.T) {
		pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
		ctx := context.Background()
		cookie := sessionCookie(t, ctx, sessions, admin)
		leaseExportClock(t, pool, "2024-06-01")
		leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
		lease, _ := leaseExportAgreement(t, pool, client, company, admin, "Corrected Scheduled agreement", "2024-07-01", "2024-12-31", 5000)
		svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
		base, err := svc.DraftLeaseBase(ctx, actor, lease)
		historyCheck(t, err)
		historyCheck(t, svc.CorrectLeaseFacts(ctx, actor, lease, authorization.LeaseFactInput{ExpectedRevision: base.Revision, StartsOn: "2024-07-02", EndsOn: "2025-01-30", PremisesID: *base.PremisesID, Factual: true, Reason: "Correct recorded commencement and term", OperationKey: newOperationKey()}))
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
		position := leaseExportKind(rows, "lease")[0]
		historyEqual(t, "corrected start", position["starts_on"], "2024-07-02")
		historyEqual(t, "corrected monthly transition", position["first_monthly_on"], "2025-01-31")
		historyEqual(t, "Scheduled has no effective coverage", len(leaseExportKind(rows, "coverage")), 0)
		bill, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
		historyEqual(t, "Scheduled excluded from rent total", leaseExportKind(bill, "summary")[0]["matching_monthly_rent"], "0.00")
		historyEqual(t, "scheduled rate retained separately", leaseExportKind(bill, "lease")[0]["scheduled_monthly_rent"], "50.00")
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "company", NoticeOn: "2024-06-01", EffectiveOn: "2024-06-01", Reason: "Cancel before corrected commencement", OperationKey: newOperationKey()}))
		rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
		historyEqual(t, "scheduled cancellation immediate", leaseExportKind(rows, "lease")[0]["lifecycle"], "terminated")
		historyEqual(t, "cancellation has no coverage", len(leaseExportKind(rows, "coverage")), 0)
		bill, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
		historyEqual(t, "cancelled total", leaseExportKind(bill, "summary")[0]["matching_monthly_rent"], "0.00")
		for _, period := range leaseExportKind(bill, "period") {
			if period["period_state"] == "pending" || period["period_state"] == "failed" {
				t.Fatalf("cancelled future queue: %v", period)
			}
		}
	})
	t.Run("inclusive corrected notice and withdrawal", func(t *testing.T) {
		pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
		ctx := context.Background()
		cookie := sessionCookie(t, ctx, sessions, admin)
		leaseExportClock(t, pool, "2024-06-01")
		leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
		lease, _ := leaseExportAgreement(t, pool, client, company, admin, "Coherent notice", "2024-01-01", "2024-12-31", 5000)
		svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-06-01", EffectiveOn: "2024-06-30", Reason: "Inclusive boundary", OperationKey: newOperationKey()}))
		leaseExportClock(t, pool, "2024-07-02")
		for _, day := range []string{"2024-06-30", "2024-07-01"} {
			op, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "as_of": {day}})
			billing, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "as_of": {day}})
			state, rent, covered := "active", "50.00", 1
			if day == "2024-07-01" {
				state, rent, covered = "terminated", "0.00", 0
			}
			historyEqual(t, "effective lifecycle", leaseExportKind(op, "lease")[0]["lifecycle"], state)
			historyEqual(t, "effective coverage", len(leaseExportKind(op, "coverage")), covered)
			historyEqual(t, "effective rent", leaseExportKind(billing, "summary")[0]["matching_monthly_rent"], rent)
			historyEqual(t, "same position in both contracts", leaseExportKind(billing, "lease")[0]["lifecycle"], state)
		}
		review, err := svc.ReviewLeaseNotice(ctx, actor, lease)
		historyCheck(t, err)
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{ExpectedRevision: review.Revision, Action: "revise", Factual: true, Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-06-01", EffectiveOn: "2024-07-31", Reason: "Correct retained end", OperationKey: newOperationKey()}))
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "as_of": {"2024-07-01"}})
		historyEqual(t, "corrected historical agreement", leaseExportKind(rows, "lease")[0]["lifecycle"], "active")
		historyEqual(t, "corrected historical coverage", len(leaseExportKind(rows, "coverage")), 1)
		review, err = svc.ReviewLeaseNotice(ctx, actor, lease)
		historyCheck(t, err)
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{ExpectedRevision: review.Revision, Action: "withdraw", Factual: true, Reason: "Notice fact withdrawn", OperationKey: newOperationKey()}))
		rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "as_of": {"2024-07-01"}})
		historyEqual(t, "withdrawn termination", leaseExportKind(rows, "lease")[0]["termination_on"], "")
	})
	t.Run("imported termination missing historical facts", func(t *testing.T) {
		pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
		ctx := context.Background()
		cookie := sessionCookie(t, ctx, sessions, admin)
		leaseExportClock(t, pool, "2024-08-01")
		c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Unknown termination history").SaveX(ctx)
		l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Known premises").SaveX(ctx)
		typ := client.AssetType.Create().SetCompanyID(company).SetName("Known equipment").SaveX(ctx)
		a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Known old coverage").SaveX(ctx)
		svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
		lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
		historyCheck(t, err)
		historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Unknown termination history", StartsOn: "2024-01-01", EndsOn: "2024-01-30", PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
		through := "2024-02-15"
		historyCheck(t, svc.WithinActor(ctx, actor, func(tx pgx.Tx, _ authorization.Principal) error {
			if err := svc.WriteLeaseDependencies(ctx, tx, actor, authorization.LeaseDependencies{LeaseID: lease.ID, CustomerID: c.ID, PremisesID: &l.ID, Lifecycle: "terminated", Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: "2024-01-01", EndsOn: &through}}}); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE leases SET lifecycle='terminated' WHERE id=$1`, lease.ID)
			return err
		}))
		before := leasePreservationRows(t, pool, []string{"leases", "lease_notice_revisions", "lease_asset_dependencies"})
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "as_of": {"2024-02-01"}})
		historyEqual(t, "unknown historical lifecycle", leaseExportKind(rows, "lease")[0]["lifecycle"], "unavailable")
		historyEqual(t, "unknown dependent total is not zero", leaseExportKind(rows, "summary")[0]["matching_monthly_rent"], "unavailable")
		for _, query := range []string{"lifecycle=active", "ending_from=2024-01-01", "placement=unplaced", "asset=" + fmt.Sprint(a.ID)} {
			values, err := url.ParseQuery(query)
			historyCheck(t, err)
			values.Set("mode", "operational")
			values.Set("as_of", "2024-02-01")
			values.Set("operation_id", newOperationKey())
			path := masterPost(t, web, cookie, leaseExportReviewPath, values, 303)
			if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
				t.Fatalf("unknown membership silently exported for %s", query)
			}
			var state string
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&state, &count))
			historyEqual(t, "unknown selection fails publication", state, "Failed")
			historyEqual(t, "unknown selection cannot assert partial totals", count, 0)
		}
		historyEqual(t, "no invented notice or historical facts", leasePreservationRows(t, pool, []string{"leases", "lease_notice_revisions", "lease_asset_dependencies"}), before)
	})
}

func TestLeaseExportFilteredSummaryAndValidationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-06-01")
	leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
	lease, assets := leaseExportAgreement(t, pool, client, company, admin, "Filtered two Assets", "2024-01-01", "2024-12-31", 5000, 2500)
	leaseExportAgreement(t, pool, client, company, admin, "Excluded rent sentinel", "2024-01-01", "2024-11-30", 9900)
	var customer, premises int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id,premises_id FROM leases WHERE id=$1`, lease).Scan(&customer, &premises))
	filters := []url.Values{
		{"search": {"Filtered two"}}, {"customer": {fmt.Sprint(customer)}}, {"premises": {fmt.Sprint(premises)}}, {"asset": {fmt.Sprint(assets[0])}},
		{"ending_from": {"2024-12-01"}, "ending_through": {"2024-12-31"}},
		{"search": {"Filtered two"}, "lifecycle": {"active"}, "customer": {fmt.Sprint(customer)}, "premises": {fmt.Sprint(premises)}, "asset": {fmt.Sprint(assets[1])}, "ending_from": {"2024-12-31"}, "ending_through": {"2024-12-31"}},
	}
	for _, values := range filters {
		values.Set("mode", "billing")
		rows, _ := leaseExportGet(t, pool, web, cookie, values)
		summary := leaseExportKind(rows, "summary")[0]
		historyEqual(t, "filtered unique count", summary["matching_lease_count"], "1")
		historyEqual(t, "rent not multiplied by Assets or Invoices", summary["matching_monthly_rent"], "75.00")
		historyEqual(t, "exact selected identity", leaseExportKind(rows, "lease")[0]["lease_id"], fmt.Sprint(lease))
		if len(leaseExportKind(rows, "rent")) != 2 || len(leaseExportKind(rows, "invoice_asset")) < 2 {
			t.Fatal("fixture lacks multiple financial children")
		}
	}
	for _, query := range []string{"ending_from=2024-12-31&ending_through=2024-01-01", "ending_from=bad", "lifecycle=bogus", "billing=bogus", "placement=bogus", "archive=bogus", "sort=bogus"} {
		expectStatus(t, web, cookie, "GET", leaseExportReviewPath+"?mode=billing&"+query, 422)
	}
	expectStatus(t, web, cookie, "GET", leaseExportReviewPath+"?mode=billing&surprise=1", 400)
	for _, query := range []string{"billing=review", "billing=pending"} {
		expectStatus(t, web, cookie, "GET", leaseExportReviewPath+"?mode=operational&"+query, 403)
	}
}

func TestLeaseExportReadyAcrossCompanyMidnight(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, filter := range []string{"pending", "failed"} {
			t.Run(fmt.Sprintf("explicit=%t/%s", explicit, filter), func(t *testing.T) {
				pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
				ctx := context.Background()
				cookie := sessionCookie(t, ctx, sessions, admin)
				leaseExportClock(t, pool, "2024-01-01")
				leaseExportInstallRecordingClock(t, pool, "2024-01-01T12:00:00Z")
				leaseExportAgreement(t, pool, client, company, admin, "Midnight queue", "2024-01-01", "2024-12-31", 5000)
				user, role := leaseExportReader(t, pool, client, company, admin, "Midnight authority control", leaseExportGrants("lease.view", "lease.export", "lease.billing.view", "invoice.view", "pricing.view", "invoice.archived.view"))
				cookie = sessionCookie(t, ctx, sessions, user)
				leaseExportClock(t, pool, "2024-01-02")
				leaseExportRecordingClock(t, pool, "2024-01-02T12:00:00Z")
				if filter == "failed" {
					_, err := pool.Exec(ctx, `CREATE FUNCTION lease_export_midnight_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'midnight queue failure'; END $$; CREATE TRIGGER lease_export_midnight_fail BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION lease_export_midnight_fail()`)
					historyCheck(t, err)
					if err := authorization.New(pool).GenerateLeaseDrafts(ctx); err == nil {
						t.Fatal("expected generation failure")
					}
				}
				values := url.Values{"mode": {"billing"}, "billing": {filter}, "operation_id": {newOperationKey()}}
				if explicit {
					values.Set("as_of", "2024-01-02")
				}
				path := leaseExportPrepare(t, pool, web, cookie, values)
				controlValues := url.Values{}
				for key, entries := range values {
					controlValues[key] = slices.Clone(entries)
				}
				controlValues.Set("operation_id", newOperationKey())
				authorityControl := leaseExportPrepare(t, pool, web, cookie, controlValues)
				before := requestBody(t, web, cookie, "GET", path+"/download", 200)
				historyEqual(t, "queue fixture matched", leaseExportKind(leaseExportRows(t, before), "summary")[0]["matching_lease_count"], "1")
				leaseExportClock(t, pool, "2024-01-03")
				assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Ready")
				historyEqual(t, "midnight cannot reclassify prepared bytes", requestBody(t, web, cookie, "GET", path+"/download", 200), before)
				requestBody(t, web, cookie, "GET", authorityControl+"/download", 200)
				historyEqual(t, "same-intent replay survives midnight", masterPost(t, web, cookie, leaseExportReviewPath, values, 303), path)
				expectStatus(t, web, cookie, "GET", leaseExportReviewPath+"?mode=billing&billing="+filter+"&as_of=2024-01-02", 422)
				masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"mode": {"billing"}, "billing": {filter}, "as_of": {"2024-01-02"}, "operation_id": {newOperationKey()}}, 422)
				fresh, freshPath := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "billing": {filter}})
				historyEqual(t, "fresh current export resolves new company date", fresh[0]["as_of"], "2024-01-03")
				_, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/"))
				historyCheck(t, err)
				expectStatus(t, web, cookie, "GET", path+"/download", 410)
				_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.billing.view'`, role)
				historyCheck(t, err)
				var unexpired bool
				historyCheck(t, pool.QueryRow(ctx, `SELECT state='Ready' AND clock_timestamp()<ready_at+interval '24 hours' FROM export_jobs WHERE id=$1`, strings.TrimPrefix(authorityControl, "/exports/")).Scan(&unexpired))
				historyEqual(t, "pre-midnight authority control still within Ready TTL", unexpired, true)
				denied := requestBody(t, web, cookie, "GET", authorityControl+"/download", 410)
				assertNotContains(t, denied, "generated_total")
				expectStatus(t, web, cookie, "GET", freshPath+"/download", 410)
			})
		}
	}
}

func TestLeaseExportReplayExpiryAndFacetLoss(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view", "customer.view", "customer.financial.view", "location.view", "asset.view", "invoice.view", "pricing.view", "settlement.view", "invoice.archived.view")
	for _, permission := range []string{"lease.view", "lease.export", "lease.billing.view", "customer.view", "location.view", "asset.view", "invoice.view", "pricing.view", "settlement.view", "invoice.archived.view"} {
		t.Run(permission, func(t *testing.T) {
			user, role := leaseExportReader(t, pool, client, company, admin, "Facet "+permission, grants)
			cookie := sessionCookie(t, ctx, sessions, user)
			values := url.Values{"mode": {"billing"}, "operation_id": {newOperationKey()}}
			path := masterPost(t, web, cookie, leaseExportReviewPath, values, 303)
			historyEqual(t, "same operation replay", masterPost(t, web, cookie, leaseExportReviewPath, values, 303), path)
			values.Set("search", "changed intent")
			masterPost(t, web, cookie, leaseExportReviewPath, values, 409)
			_, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			requestBody(t, web, cookie, "GET", path+"/download", 200)
			_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, permission)
			historyCheck(t, err)
			expectStatus(t, web, cookie, "GET", path+"/download", 410)
		})
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := leaseExportPrepare(t, pool, web, cookie, url.Values{"mode": {"operational"}})
	_, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/"))
	historyCheck(t, err)
	expectStatus(t, web, cookie, "GET", path+"/download", 410)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "This result is unavailable")
	var status exports.Status
	masterJSON(t, web, cookie, path, &status)
	historyEqual(t, "Ready artifact expired flag", status.Expired, true)
}

func TestLeaseExportBillingProvenanceAndIndependentFacets(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice, asset, customer int64
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.id,d.asset_id,l.customer_id,lease_dependency_today(l.company_id)::text FROM invoices i JOIN leases l ON l.id=i.lease_id JOIN lease_asset_dependencies d ON d.lease_id=l.id WHERE l.id=$1`, lease).Scan(&invoice, &asset, &customer, &today))
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	base, err := svc.DraftLeaseBase(ctx, actor, lease)
	historyCheck(t, err)
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, authorization.LeaseBillingRevisionInput{EffectiveOn: base.StartsOn, Reason: "Retained rent and tax revision", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 7500, TaxRate: "7"}}}))
	adjust := fmt.Sprintf("/leases/%d/billing/adjustments?invoice_id=%d", lease, invoice)
	review := requestBody(t, web, cookie, "GET", adjust, 200)
	fieldPost(t, web, cookie, adjust, url.Values{"invoice_id": {fmt.Sprint(invoice)}, "asset_id": {fmt.Sprint(asset)}, "kind": {"abatement"}, "amount_cents": {"1"}, "starts_on": {today}, "ends_on": {today}, "reason": {"Export retained allowance"}, "operation_key": {jobExecutionHidden(t, review, "operation_key")}, "invoice_revision": {jobExecutionHidden(t, review, "invoice_revision")}}, 303)
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice))
	finance, financeActor := settlement.New(pool), settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	// The adjustment changes the live Invoice to 1.05; generation stays 1.06.
	payment, err := finance.RecordPayment(ctx, financeActor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, AmountCents: 105, Method: settlement.Cash, ReceivedDate: settlement.Date(today)})
	historyCheck(t, err)
	_, err = finance.ReversePayment(ctx, financeActor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment.ID, InvoiceID: invoice, EffectiveDate: settlement.Date(today), Reason: "Retained reversal"})
	historyCheck(t, err)
	actor.Channel = "web"
	receipt, err := finance.RecordCustomerPayment(ctx, actor, customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 105, Method: settlement.Cash, ReceivedDate: settlement.Date(today)})
	historyCheck(t, err)
	var credit uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, receipt.ID).Scan(&credit))
	application, err := finance.ApplyCredit(ctx, financeActor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, CreditID: credit, RequestedCents: 105, EffectiveDate: settlement.Date(today)})
	historyCheck(t, err)
	tables := []string{"leases", "lease_finalizations", "lease_billing_revisions", "lease_manual_adjustments", "lease_billing_obligations", "lease_billing_obligation_revisions", "lease_coverage_events", "lease_coverage_revisions", "lease_invoice_generations", "invoices", "invoice_payments", "payment_invoice_allocations", "customer_credits", "credit_applications", "settlement_reversals"}
	before := leasePreservationRows(t, pool, tables)
	rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	generated := leaseExportKind(rows, "invoice")
	historyEqual(t, "one generation", len(generated), 1)
	historyEqual(t, "original generation amount", generated[0]["generated_total"], "1.06")
	historyEqual(t, "original generation tax", generated[0]["generated_tax"], "0.05")
	historyEqual(t, "ordinary effective status", generated[0]["effective_invoice_status"], "invoice:paid")
	historyEqual(t, "ordinary manual status", generated[0]["manual_invoice_status"], "invoice:invoiced")
	revisions := leaseExportKind(rows, "billing_revision")
	historyEqual(t, "one retained revision", len(revisions), 1)
	for key, want := range map[string]string{"before_rent": "50.00", "after_rent": "75.00", "before_tax_rate": "5", "after_tax_rate": "7", "actor_id": fmt.Sprint(admin), "asset_id": fmt.Sprint(asset), "revision_line_ordinal": "1", "reason": "Retained rent and tax revision"} {
		historyEqual(t, key, revisions[0][key], want)
	}
	adjustments := leaseExportKind(rows, "adjustment")
	historyEqual(t, "retained adjustment count", len(adjustments), 1)
	historyEqual(t, "adjustment target", adjustments[0]["invoice_id"], fmt.Sprint(invoice))
	historyEqual(t, "adjustment provenance", adjustments[0]["reason"], "Export retained allowance")
	settlements := leaseExportKind(rows, "settlement")
	historyEqual(t, "ordinary settlement facts not duplicated", len(settlements), 2)
	assertSettlementIDs := func(entries []map[string]string) {
		t.Helper()
		var got []string
		for _, row := range entries {
			got = append(got, row["settlement_id"])
		}
		want := []string{payment.ID.String(), application.ID.String()}
		slices.Sort(got)
		slices.Sort(want)
		historyEqual(t, "exact distinct Payment and Credit Application identities", got, want)
	}
	assertSettlementIDs(settlements)
	for _, row := range settlements {
		historyEqual(t, "settlement child does not multiply generation", row["generated_total"], "")
		historyEqual(t, "exact settlement amount", row["settlement_amount"], "1.05")
		historyEqual(t, "exact settlement effective date", row["settlement_effective_on"], today)
		if row["settlement_id"] == payment.ID.String() {
			historyEqual(t, "payment kind", row["settlement_kind"], "Payment")
			historyEqual(t, "payment reversal date", row["reversed_on"], today)
		} else {
			historyEqual(t, "credit application identity", row["settlement_id"], application.ID.String())
			historyEqual(t, "credit application kind", row["settlement_kind"], "Payment-funded Credit application")
			historyEqual(t, "unreversed application", row["reversed_on"], "")
		}
	}
	historyEqual(t, "export has no settlement/domain effects", leasePreservationRows(t, pool, tables), before)
	_, err = finance.ReverseCreditApplication(ctx, financeActor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: application.ID, InvoiceID: invoice, EffectiveDate: settlement.Date(today), Reason: "Export credit reversal provenance"})
	historyCheck(t, err)
	before = leasePreservationRows(t, pool, tables)
	reversed, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	assertSettlementIDs(leaseExportKind(reversed, "settlement"))
	for _, row := range leaseExportKind(reversed, "settlement") {
		historyEqual(t, "both ordinary reversals retain effective date", row["reversed_on"], today)
		historyEqual(t, "reversal retains source amount", row["settlement_amount"], "1.05")
		historyEqual(t, "reversal retains source effective date", row["settlement_effective_on"], today)
	}
	historyEqual(t, "reversal restores ordinary Invoice status", leaseExportKind(reversed, "invoice")[0]["effective_invoice_status"], "invoice:invoiced")
	historyEqual(t, "reversal export preserves full settlement rows", leasePreservationRows(t, pool, tables), before)
	user, _ := leaseExportReader(t, pool, client, company, admin, "Billing without independent facets", leaseExportGrants("lease.view", "lease.export", "lease.billing.view", "invoice.view", "pricing.view"))
	redacted, _ := leaseExportGet(t, pool, web, sessionCookie(t, ctx, sessions, user), url.Values{"mode": {"billing"}})
	historyEqual(t, "Settlement independently omitted", len(leaseExportKind(redacted, "settlement")), 0)
	historyEqual(t, "settlement-derived status unavailable", leaseExportKind(redacted, "invoice")[0]["effective_invoice_status"], "unavailable")
	for _, kind := range []string{"billing_revision", "adjustment", "invoice_asset", "rent"} {
		for _, row := range leaseExportKind(redacted, kind) {
			historyEqual(t, "hidden Asset", row["asset_id"], "")
			historyEqual(t, "hidden actor", row["actor_id"], "")
		}
	}
	periods := leaseExportKind(redacted, "period")
	historyEqual(t, "negative generation authority unavailable", len(periods), 1)
	historyEqual(t, "negative generation placeholder", periods[0]["period_state"], "unavailable")
	for _, missing := range []string{"invoice.view", "pricing.view"} {
		grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view")
		if missing == "invoice.view" {
			grants = append(grants, leaseExportGrants("job.view", "pricing.view")...)
		} else {
			grants = append(grants, leaseExportGrants("invoice.view")...)
		}
		user, _ := leaseExportReader(t, pool, client, company, admin, "Missing "+missing, grants)
		cookie := sessionCookie(t, ctx, sessions, user)
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
		for _, kind := range []string{"invoice", "invoice_asset", "adjustment", "settlement"} {
			historyEqual(t, "populated independent facet redaction "+kind, len(leaseExportKind(rows, kind)), 0)
		}
		for _, predicate := range []string{"review", "void"} {
			masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"mode": {"billing"}, "billing": {predicate}, "operation_id": {newOperationKey()}}, 403)
		}
	}
	directoryGrants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view", "invoice.view", "pricing.view", "directory.view")
	actorReader, actorRole := leaseExportReader(t, pool, client, company, admin, "Actor provenance reference", directoryGrants)
	actorCookie := sessionCookie(t, ctx, sessions, actorReader)
	actorRows, actorPath := leaseExportGet(t, pool, web, actorCookie, url.Values{"mode": {"billing"}})
	historyEqual(t, "actor fixture disclosed", leaseExportKind(actorRows, "billing_revision")[0]["actor_id"], fmt.Sprint(admin))
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='directory.view'`, actorRole)
	historyCheck(t, err)
	expectStatus(t, web, actorCookie, "GET", actorPath+"/download", 410)
}

func TestLeaseExportCoveragePlacementAndArchiveHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-06-01")
	leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
	lease, assets := leaseExportAgreement(t, pool, client, company, admin, "Placement remains independent", "2024-01-01", "2024-06-29", 5000)
	var customer, premises int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id,premises_id FROM leases WHERE id=$1`, lease).Scan(&customer, &premises))
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	historyCheck(t, svc.ChangeAssetAssociation(ctx, actor, assets[0], authorization.AssetAssociationInput{Action: "place", EffectiveDate: "2024-06-01", Reason: "Correct premises", CustomerID: customer, LocationID: premises}))
	leaseExportClock(t, pool, "2024-06-10")
	historyCheck(t, svc.ChangeAssetAssociation(ctx, actor, assets[0], authorization.AssetAssociationInput{Action: "return", EffectiveDate: "2024-06-10", Reason: "Return does not end coverage", Revision: 1}))
	rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "placement": {"unplaced"}})
	historyEqual(t, "return retains coverage", len(leaseExportKind(rows, "coverage")), 1)
	wrong := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer).SetTitle("Other premises").SaveX(ctx)
	leaseExportClock(t, pool, "2024-06-20")
	historyCheck(t, svc.ChangeAssetAssociation(ctx, actor, assets[0], authorization.AssetAssociationInput{Action: "place", EffectiveDate: "2024-06-20", Reason: "Wrong premises is distinct", Revision: 2, CustomerID: customer, LocationID: wrong.ID}))
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-06-20", EffectiveOn: "2024-06-30", Reason: "Lease ends without Asset return", OperationKey: newOperationKey()}))
	leaseExportClock(t, pool, "2024-07-01")
	for _, test := range []struct {
		day, placement, state string
		covered               int
	}{{"2024-06-01", "placed", "active", 1}, {"2024-06-10", "unplaced", "active", 1}, {"2024-06-20", "wrong_premises", "active", 1}, {"2024-06-30", "wrong_premises", "month_to_month", 1}, {"2024-07-01", "", "terminated", 0}} {
		rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "as_of": {test.day}})
		historyEqual(t, "calendar without worker", leaseExportKind(rows, "lease")[0]["lifecycle"], test.state)
		historyEqual(t, "first monthly transition", leaseExportKind(rows, "lease")[0]["first_monthly_on"], "2024-06-30")
		historyEqual(t, "coverage inclusive boundary", len(leaseExportKind(rows, "coverage")), test.covered)
		if test.covered == 1 {
			historyEqual(t, "historical placement", leaseExportKind(rows, "coverage")[0]["placement"], test.placement)
		}
	}
	var location int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT location_id FROM asset_association_periods WHERE asset_id=$1 AND ends_before IS NULL`, assets[0]).Scan(&location))
	historyEqual(t, "termination never returns Asset", location, wrong.ID)
	fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d/delete", lease), nil, 303)
	before := leasePreservationRows(t, pool, []string{"leases", "lease_asset_dependencies", "asset_association_periods", "lease_invoice_generations", "invoices"})
	rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "archive": {"archived"}})
	historyEqual(t, "archived terminated history", leaseExportKind(rows, "lease")[0]["lifecycle"], "terminated")
	historyEqual(t, "archive does not revive coverage", len(leaseExportKind(rows, "coverage")), 0)
	historyEqual(t, "archive export does not revive domain", leasePreservationRows(t, pool, []string{"leases", "lease_asset_dependencies", "asset_association_periods", "lease_invoice_generations", "invoices"}), before)
}

func TestLeaseExportQueueEntitlementBoundaries(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-01-01")
	leaseExportInstallRecordingClock(t, pool, "2024-01-01T12:00:00Z")
	active, _ := leaseExportAgreement(t, pool, client, company, admin, "Retained owed period", "2024-01-01", "2024-12-31", 5000)
	leaseExportAgreement(t, pool, client, company, admin, "Scheduled initial period", "2024-02-01", "2024-12-31", 2500)
	rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	var future int
	for _, period := range leaseExportKind(rows, "period") {
		if period["due_on"] == "2024-02-01" {
			historyEqual(t, "31 days not eligible", period["period_state"], "not_yet_eligible")
			future++
		}
	}
	if future == 0 {
		t.Fatal("retained future obligation omitted")
	}
	leaseExportClock(t, pool, "2024-01-02")
	rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "billing": {"pending"}})
	historyEqual(t, "30-day window includes Active and Scheduled obligations", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "2")
	// Both selections are now historical. The same retained February obligation
	// is outside the window on January 1 (+31), inside on January 2 (+30).
	// Missing historical worker status must not erase the eligibility boundary.
	leaseExportClock(t, pool, "2024-01-03")
	var obligation int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly' AND due_on='2024-02-01'`, active).Scan(&obligation))
	for _, selection := range []struct{ day, state string }{{"2024-01-01", "not_yet_eligible"}, {"2024-01-02", "ungenerated"}} {
		historical, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "as_of": {selection.day}, "search": {"Retained owed period"}})
		historyEqual(t, "past failure facts remain unavailable", leaseExportKind(historical, "lease")[0]["failure_history"], "unavailable")
		matches := 0
		for _, period := range leaseExportKind(historical, "period") {
			if period["period_kind"] == "monthly" && period["due_on"] == "2024-02-01" {
				matches++
				historyEqual(t, "historical retained obligation identity", period["obligation_id"], fmt.Sprint(obligation))
				historyEqual(t, "historical +30 eligibility boundary", period["period_state"], selection.state)
			}
		}
		historyEqual(t, "one retained future obligation", matches, 1)
	}
	// Let February become owed without running generation, then terminate. The
	// old owed period remains pending; future unowed periods must not match.
	leaseExportClock(t, pool, "2024-02-02")
	historyCheck(t, authorization.New(pool).ChangeLeaseNotice(ctx, authorization.Actor{UserID: admin}, active, authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-02-02", EffectiveOn: "2024-02-02", Reason: "Retain prior owed period", OperationKey: newOperationKey()}))
	leaseExportClock(t, pool, "2024-02-03")
	rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "billing": {"pending"}, "search": {"Retained owed period"}})
	historyEqual(t, "Terminated agreement retains owed pending period", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "1")
	var owed bool
	for _, period := range leaseExportKind(rows, "period") {
		if period["due_on"] == "2024-02-01" {
			historyEqual(t, "old owed period", period["period_state"], "pending")
			owed = true
		}
		if period["due_on"] > "2024-02-02" && period["period_state"] == "pending" {
			t.Fatalf("unowed future period pending: %v", period)
		}
	}
	if !owed {
		t.Fatal("old owed period was lost")
	}
}

func TestLeaseExportArchivedOriginalFacets(t *testing.T) {
	for _, related := range []struct{ table, permission string }{{"assets", "asset.archived.view"}, {"locations", "location.archived.view"}, {"invoices", "invoice.archived.view"}} {
		t.Run(related.table, func(t *testing.T) {
			pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
			var asset, premises, invoice int64
			var today string
			historyCheck(t, pool.QueryRow(ctx, `SELECT d.asset_id,l.premises_id,i.id,lease_dependency_today(l.company_id)::text FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id JOIN invoices i ON i.lease_id=l.id WHERE l.id=$1`, lease).Scan(&asset, &premises, &invoice, &today))
			historyCheck(t, authorization.New(pool).ChangeLeaseNotice(ctx, authorization.Actor{UserID: admin}, lease, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "company", NoticeOn: today, EffectiveOn: today, Reason: "Close operational blockers", OperationKey: newOperationKey()}))
			id := map[string]int64{"assets": asset, "locations": premises, "invoices": invoice}[related.table]
			if related.table == "invoices" {
				flow, actor := statusflow.New(pool), statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
				historyCheck(t, flow.FinalizeInvoice(ctx, actor, invoice))
				var void int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&void))
				historyCheck(t, flow.TransitionInvoice(ctx, actor, invoice, void))
			}
			_, err := pool.Exec(ctx, `UPDATE `+pgx.Identifier{related.table}.Sanitize()+` SET deleted_at=clock_timestamp() WHERE id=$1`, id)
			historyCheck(t, err)
			grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view", "asset.view", "location.view", "invoice.view", "pricing.view", "asset.archived.view", "location.archived.view", "invoice.archived.view")
			user, role := leaseExportReader(t, pool, client, company, admin, "Retained archived facet", grants)
			reader := sessionCookie(t, ctx, sessions, user)
			rows, path := leaseExportGet(t, pool, web, reader, url.Values{"mode": {"billing"}})
			if len(leaseExportKind(rows, "invoice")) != 1 {
				t.Fatal("archived-related fixture lacks captured generation")
			}
			_, err = pool.Exec(ctx, `UPDATE `+pgx.Identifier{related.table}.Sanitize()+` SET deleted_at=NULL WHERE id=$1`, id)
			historyCheck(t, err)
			_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, related.permission)
			historyCheck(t, err)
			expectStatus(t, web, reader, "GET", path+"/download", 410)
		})
	}
}

func TestLeaseExportFutureCoverageReplacementHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	leaseExportClock(t, pool, "2024-06-01")
	lease, assets := leaseExportAgreement(t, pool, client, company, admin, "Effective coverage replacements", "2024-01-01", "2024-12-31", 5000)
	var typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_type_id FROM assets WHERE id=$1`, assets[0]).Scan(&typ))
	replacement := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Replacement starts June 10").SaveX(ctx)
	addition := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Addition starts June 20").SaveX(ctx)
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	_, err := svc.ChangeLeaseCoverage(ctx, actor, lease, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: "2024-06-10", OutgoingID: assets[0], IncomingID: replacement.ID}, Action: "create", Reason: "Atomic effective replacement", OperationKey: newOperationKey()})
	historyCheck(t, err)
	review, err := svc.ReviewLeaseCoverage(ctx, actor, lease)
	historyCheck(t, err)
	rent := int64(2500)
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, authorization.LeaseCoverageInput{ExpectedRevision: review.Revision, LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: "2024-06-20", IncomingID: addition.ID}, Action: "create", Reason: "Future effective addition", OperationKey: newOperationKey(), RentCents: &rent, TaxRate: "5"})
	historyCheck(t, err)
	leaseExportClock(t, pool, "2024-06-30")
	for _, test := range []struct {
		day    string
		assets []int64
	}{{"2024-06-09", assets}, {"2024-06-10", []int64{replacement.ID}}, {"2024-06-19", []int64{replacement.ID}}, {"2024-06-20", []int64{replacement.ID, addition.ID}}} {
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "as_of": {test.day}})
		var got []int64
		for _, row := range leaseExportKind(rows, "coverage") {
			id, err := strconv.ParseInt(row["asset_id"], 10, 64)
			historyCheck(t, err)
			got = append(got, id)
		}
		slices.Sort(got)
		want := slices.Clone(test.assets)
		slices.Sort(want)
		historyEqual(t, "effective Assets on "+test.day, got, want)
	}
}

func TestLeaseExportVoidReplacementAndWithdrawnCharge(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	leaseExportClock(t, pool, "2024-01-01")
	leaseExportInstallRecordingClock(t, pool, "2024-01-01T12:00:00Z")
	lease, assets := leaseExportAgreement(t, pool, client, company, admin, "Void replacement provenance", "2024-01-01", "2024-12-31", 5000)
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-02-01", Reason: "Reconcile pending obligation", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: assets[0], RentCents: 6000, TaxRate: "5"}}}))
	leaseExportClock(t, pool, "2024-01-02")
	leaseExportRecordingClock(t, pool, "2024-01-02T12:00:00Z")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var obligation, invoice, revision, customer int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT o.id,g.invoice_id,g.obligation_revision_id,l.customer_id FROM lease_billing_obligations o JOIN leases l ON l.id=o.lease_id JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-02-01'`, lease).Scan(&obligation, &invoice, &revision, &customer))
	if revision == 0 {
		t.Fatal("fixture requires actual reconciled obligation revision")
	}
	flow, fa := statusflow.New(pool), statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
	historyCheck(t, flow.FinalizeInvoice(ctx, fa, invoice))
	var void int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&void))
	historyCheck(t, flow.TransitionInvoice(ctx, fa, invoice, void))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var generated int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations WHERE obligation_id=$1`, obligation).Scan(&generated))
	historyEqual(t, "Void never auto-replaced", generated, 1)
	rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}, "billing": {"void"}})
	for _, row := range leaseExportKind(rows, "invoice") {
		if row["invoice_id"] == fmt.Sprint(invoice) {
			historyEqual(t, "original consumed Void", row["manual_invoice_status"], "invoice:void")
			historyEqual(t, "captured revision identity", row["obligation_revision_id"], fmt.Sprint(revision))
			historyEqual(t, "original revised generation", row["generated_total"], "63.00")
		}
	}
	assertNoPending := func(rows []map[string]string) {
		t.Helper()
		for _, period := range leaseExportKind(rows, "period") {
			if period["due_on"] == "2024-02-01" && period["period_kind"] == "monthly" && slices.Contains([]string{"pending", "failed", "ungenerated"}, period["period_state"]) {
				t.Fatalf("consumed Void period falsely pending: %v", period)
			}
		}
	}
	assertNoPending(rows)
	// Positive filters require only compatible Assigned Invoice/Pricing, not
	// complete All Records negative-generation knowledge.
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer).SetJobType("Assigned positive exception").SaveX(ctx)
	_, err := pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, job.ID, invoice)
	historyCheck(t, err)
	grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view")
	for _, permission := range []string{"job.view", "invoice.view", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.Assigned})
	}
	reader, _ := leaseExportReader(t, pool, client, company, admin, "Assigned positive filters", grants)
	_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, reader)
	historyCheck(t, err)
	// Replace while the approved financial source is still compatible. A later
	// revision flags both retained generations; it must not be used to bypass
	// the owning writer's incompatible-source rejection.
	path := fmt.Sprintf("/leases/%d/billing/periods/%d", lease, obligation)
	review := requestBody(t, web, cookie, "GET", path, 200)
	values := url.Values{}
	for _, key := range []string{"invoice_id", "review_hash", "operation_key"} {
		values.Set(key, jobExecutionHidden(t, review, key))
	}
	fieldPost(t, web, cookie, path+"/replace", values, 303)
	fieldPost(t, web, cookie, path+"/replace", values, 303)
	var successor int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM lease_invoice_generations WHERE predecessor_invoice_id=$1`, invoice).Scan(&successor))
	leaseExportClock(t, pool, "2024-01-03")
	leaseExportRecordingClock(t, pool, "2024-01-03T12:00:00Z")
	billingReview, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease)
	historyCheck(t, err)
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, authorization.LeaseBillingRevisionInput{ExpectedRevision: billingReview.Revision, EffectiveOn: "2024-02-01", Reason: "Flag retained generated exception", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: assets[0], RentCents: 7000, TaxRate: "5"}}}))
	for _, filter := range []string{"void", "review"} {
		selected, _ := leaseExportGet(t, pool, web, sessionCookie(t, ctx, sessions, reader), url.Values{"mode": {"billing"}, "billing": {filter}})
		historyEqual(t, "Assigned positive membership", leaseExportKind(selected, "summary")[0]["matching_lease_count"], "1")
		invoices := leaseExportKind(selected, "invoice")
		historyEqual(t, "Assigned witness only", len(invoices), 1)
		historyEqual(t, "Assigned witness identity", invoices[0]["invoice_id"], fmt.Sprint(invoice))
		historyEqual(t, "Assigned review fact", invoices[0]["review_required"], "true")
	}
	var typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_type_id FROM assets WHERE id=$1`, assets[0]).Scan(&typ))
	added := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Withdrawn optional charge").SaveX(ctx)
	rent, charge := int64(1000), int64(100)
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: "2024-01-05", IncomingID: added.ID}, Action: "create", Reason: "Optional uncommenced charge", OperationKey: newOperationKey(), RentCents: &rent, ChargeCents: &charge, TaxRate: "5"})
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `CREATE FUNCTION lease_export_optional_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.line_items::jsonb->0->>'charge_kind'='coverage' THEN RAISE EXCEPTION 'optional charge fixture failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER lease_export_optional_fault BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION lease_export_optional_fault()`)
	historyCheck(t, err)
	if err := svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("expected optional charge failure")
	}
	coverage, err := svc.ReviewLeaseCoverage(ctx, actor, lease)
	historyCheck(t, err)
	withdraw := coverage.Events[0].LeaseCoverageInput
	withdraw.ExpectedRevision, withdraw.Action, withdraw.Reason, withdraw.OperationKey = coverage.Revision, "withdraw", "Never commenced", newOperationKey()
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, withdraw)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `DROP TRIGGER lease_export_optional_fault ON invoices`)
	historyCheck(t, err)
	tables := []string{"lease_billing_obligations", "lease_billing_obligation_revisions", "lease_billing_revisions", "lease_invoice_generations", "invoices", "lease_coverage_events", "lease_coverage_revisions"}
	before := leasePreservationRows(t, pool, tables)
	rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	assertNoPending(rows)
	seen := map[string]bool{}
	for _, row := range leaseExportKind(rows, "invoice") {
		seen[row["invoice_id"]] = true
	}
	if !seen[fmt.Sprint(invoice)] || !seen[fmt.Sprint(successor)] {
		t.Fatal("export lost original/successor generation provenance")
	}
	var generationLineKeys []string
	for _, row := range leaseExportKind(rows, "invoice_asset") {
		if row["invoice_id"] == fmt.Sprint(invoice) || row["invoice_id"] == fmt.Sprint(successor) {
			generationLineKeys = append(generationLineKeys, row["invoice_id"]+":"+row["line_ordinal"])
		}
		var obligationID, revisionID int64
		var amount, assetID, billingRevision string
		var taxable bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT o.id,coalesce(g.obligation_revision_id,0),line->>'unit_price_decimal',line->>'asset_id',coalesce(line->>'lease_billing_revision_id',''),(line->>'taxable')::boolean FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id LEFT JOIN lease_billing_obligation_revisions r ON r.id=g.obligation_revision_id CROSS JOIN LATERAL jsonb_array_elements(coalesce(r.line_items,o.line_items)) WITH ORDINALITY AS lines(line,ordinal) WHERE g.invoice_id=$1 AND ordinal=$2`, row["invoice_id"], row["line_ordinal"]).Scan(&obligationID, &revisionID, &amount, &assetID, &billingRevision, &taxable))
		for key, want := range map[string]string{"obligation_id": fmt.Sprint(obligationID), "obligation_revision_id": fmt.Sprint(revisionID), "line_amount": amount, "asset_id": assetID, "line_taxable": strconv.FormatBool(taxable), "line_billing_revision_id": billingRevision} {
			historyEqual(t, "captured line linkage "+key, row[key], want)
		}
	}
	wantLineKeys := []string{fmt.Sprintf("%d:1", invoice), fmt.Sprintf("%d:1", successor)}
	slices.Sort(generationLineKeys)
	slices.Sort(wantLineKeys)
	historyEqual(t, "exact original and successor single-Asset captured line set", generationLineKeys, wantLineKeys)
	withdrawn := false
	for _, period := range leaseExportKind(rows, "period") {
		if period["period_kind"] == "coverage" && period["due_on"] == "2024-01-05" {
			withdrawn = true
			historyEqual(t, "withdrawn charge not owed", period["period_state"], "not_owed")
			assertContains(t, period["reason"], "no charge owed")
		}
	}
	if !withdrawn {
		t.Fatal("withdrawn retained optional charge provenance omitted")
	}
	historyEqual(t, "export preserves populated commercial and coverage history", leasePreservationRows(t, pool, tables), before)
}

func TestLeaseExportRealCompanyTimezoneBoundary(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	// These real IANA zones differ by 26 hours, hence always straddle a civil
	// date. Do not replace lease_dependency_today: exercise its production SQL.
	west, east := "Etc/GMT+12", "Etc/GMT-14"
	eastLocation, err := time.LoadLocation(east)
	historyCheck(t, err)
	var instant time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&instant))
	start := instant.In(eastLocation).Format(time.DateOnly)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET timezone=$1 WHERE company_id=$2`, west, company)
	historyCheck(t, err)
	leaseExportAgreement(t, pool, client, company, admin, "Real timezone boundary", start, instant.In(eastLocation).AddDate(1, 0, 0).Format(time.DateOnly), 5000)
	dates := map[string]string{}
	for _, zone := range []string{west, east} {
		_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone=$1 WHERE company_id=$2`, zone, company)
		historyCheck(t, err)
		rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
		location, err := time.LoadLocation(zone)
		historyCheck(t, err)
		snapshot, err := time.Parse(time.RFC3339Nano, rows[0]["snapshot_at"])
		historyCheck(t, err)
		day := snapshot.In(location).Format(time.DateOnly)
		dates[zone] = day
		historyEqual(t, "company date from real snapshot instant", rows[0]["as_of"], day)
		historyEqual(t, "snapshot timezone", rows[0]["timezone"], zone)
		state, covered := "active", 1
		if day < start {
			state, covered = "scheduled", 0
		}
		historyEqual(t, "timezone-derived lifecycle without worker", leaseExportKind(rows, "lease")[0]["lifecycle"], state)
		historyEqual(t, "timezone-derived effective coverage", len(leaseExportKind(rows, "coverage")), covered)
	}
	if dates[west] >= dates[east] {
		t.Fatalf("real timezone oracle did not straddle civil date: %v", dates)
	}
}

func TestLeaseExportEmptyArchivedSelectionSurvivesRestoreAsProtectedFact(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Originally archived selected Customer").SaveX(ctx)
	_, err := pool.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID)
	historyCheck(t, err)
	user, role := leaseExportReader(t, pool, client, company, admin, "Empty archived selected input", leaseExportGrants("lease.view", "lease.export", "customer.view", "customer.archived.view"))
	cookie := sessionCookie(t, ctx, sessions, user)
	rows, path := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "customer": {fmt.Sprint(c.ID)}, "search": {"NO LEASE MATCH"}})
	historyEqual(t, "empty selection fixture", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "0")
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=NULL WHERE id=$1`, c.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.archived.view'`, role)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "GET", path+"/download", 410)
	// Current selected input is now ordinary and remains independently usable.
	rows, _ = leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "customer": {fmt.Sprint(c.ID)}, "search": {"NO LEASE MATCH"}})
	historyEqual(t, "fresh active selection authorized", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "0")
}

func TestLeaseExportArchivedDraftHistoryAndSuccessfulAudit(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	leaseExportClock(t, pool, "2024-06-01")
	leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archived Draft customer").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Original archived Draft title"}))
	leaseExportClock(t, pool, "2024-06-02")
	leaseExportRecordingClock(t, pool, "2024-06-02T12:00:00Z")
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Revision: 1, Title: "Current archived Draft title"}))
	historyCheck(t, svc.LeaseLifecycle(ctx, actor, lease.ID, false))
	user, role := leaseExportReader(t, pool, client, company, admin, "Archived Draft exporter", leaseExportGrants("lease.view", "lease.export", "lease.archived.view"))
	cookie := sessionCookie(t, ctx, sessions, user)
	for _, test := range []struct{ at, title string }{{"", "Current archived Draft title"}, {"2024-06-01", "Original archived Draft title"}} {
		rows, path := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}, "archive": {"archived"}, "as_of": {test.at}})
		row := leaseExportKind(rows, "lease")[0]
		for key, want := range map[string]string{"title": test.title, "lifecycle": "draft", "archived": "true"} {
			historyEqual(t, "archived Draft "+key, row[key], want)
		}
		historyEqual(t, "Draft never creates coverage", len(leaseExportKind(rows, "coverage")), 0)
		for _, event := range []string{"accepted", "generated", "downloaded"} {
			var actorID, roleID, initiator int64
			var action, scope, channel, executor, kind string
			historyCheck(t, pool.QueryRow(ctx, `SELECT actor_id,role_id,action,scope,channel,(detail->>'initiator_id')::bigint,detail->>'executor',detail->>'kind' FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'=$2`, strings.TrimPrefix(path, "/exports/"), event).Scan(&actorID, &roleID, &action, &scope, &channel, &initiator, &executor, &kind))
			historyEqual(t, "audit initiating actor", actorID, user)
			historyEqual(t, "audit effective Role", roleID, role)
			historyEqual(t, "audit permission", action, "lease.export")
			historyEqual(t, "audit All Records scope", scope, "A")
			historyEqual(t, "audit initiator", initiator, user)
			historyEqual(t, "audit contract", kind, "lease-operational-A-v1")
			wantChannel, wantExecutor := "web", "requester"
			if event == "generated" {
				wantChannel, wantExecutor = "worker", "system"
			}
			historyEqual(t, "audit execution channel", channel, wantChannel)
			historyEqual(t, "audit execution identity", executor, wantExecutor)
		}
	}
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.archived.view'`, role)
	historyCheck(t, err)
	masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"mode": {"operational"}, "archive": {"archived"}, "operation_id": {newOperationKey()}}, 403)
	rows, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"operational"}})
	historyEqual(t, "default excludes archived Draft", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "0")
}

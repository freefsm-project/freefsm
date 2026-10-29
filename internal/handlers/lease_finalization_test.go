package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/jackc/pgx/v5"
)

func TestLeaseFinalizationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Finalization Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises snapshot").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Unplaced").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	date := today.Format(time.DateOnly)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {date}, "ends_on": {today.AddDate(1, 0, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	// Use the established service for prospective terms, then exercise finalization
	// through the production HTTP route with an ordinary explicitly granted Role.
	amount, rent := int64(101), int64(5000)
	svc := authorization.New(pool)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, date, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &rent, TaxRate: "5"}, 0))
	grants := []authorization.Grant{}
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "asset.view"} {
		grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease finalizer", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Finalizer").SetEmail("finalizer66@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie = sessionCookie(t, ctx, sessions, u.ID)
	values := url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {date}}
	// Inject a late failure after numbering and generation, proving all-or-zero.
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_finalization_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.finalize' THEN RAISE EXCEPTION 'injected finalization audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_finalization_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_finalization_audit()`)
	historyCheck(t, err)
	tables := []string{"leases", "lease_master_dependencies", "lease_asset_dependencies", "lease_finalizations", "lease_billing_obligations", "lease_invoice_generations", "invoices", "company_settings", "issued_document_numbers", "authorization_audit"}
	rows := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var value string
			historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&value))
			out[table] = value
		}
		return out
	}
	before := rows()
	fieldPost(t, web, cookie, path+"/finalize", values, 409)
	historyEqual(t, "failed finalization including number conservation", rows(), before)
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_finalization_audit ON authorization_audit`)
	historyCheck(t, err)
	queuedChildPost(t, pool, web, cookie, path+"/finalize", `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.billing.manage'`, []any{role}, 403, values)
	historyEqual(t, "authority revoked while waiting", rows(), before)
	_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'lease.billing.manage','A')`, role)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path+"/finalize", values, 303)
	after := rows()
	fieldPost(t, web, cookie, path+"/finalize", values, 303)
	historyEqual(t, "replayed finalization", rows(), after)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "is unplaced")
	changed := url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {today.AddDate(0, 0, 1).Format(time.DateOnly)}}
	fieldPost(t, web, cookie, path+"/finalize", changed, 409)
	historyEqual(t, "changed finalization intent", rows(), after)
	var invoice int64
	var financial, category string
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.id,i.financial,s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.kind='initial'`).Scan(&invoice, &financial, &category))
	snapshot, err := commercial.Read(financial)
	historyCheck(t, err)
	if snapshot.Result.TotalCents != 106 || category != "invoice:draft" {
		t.Fatalf("ordinary Draft finance: %s %+v", category, snapshot.Result)
	}
	var coherent bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=$1 AND asset_id=$2 AND ends_on IS NULL) AND EXISTS(SELECT 1 FROM assets WHERE id=$2 AND customer_id IS NULL) AND EXISTS(SELECT 1 FROM lease_billing_obligations o WHERE lease_id=$1 AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id))`, id, a.ID).Scan(&coherent))
	if !coherent {
		t.Fatal("coverage, placement or durable pending obligation incoherent")
	}
	requestBody(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", invoice), 403)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, adminCookie, fmt.Sprintf("/invoices/%d/pdf/save", invoice), url.Values{}, 303)
	var pdf int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions WHERE document_id=$1 AND document_type='invoice'`, invoice).Scan(&pdf))
	for _, suffix := range []string{"", "/finalize", "/email"} {
		form := url.Values{}
		if suffix == "/email" {
			form = url.Values{"pdf_version": {fmt.Sprint(pdf)}, "idempotency_key": {newOperationKey()}, "to": {"recipient@example.test"}, "subject": {"Review"}, "body": {"Draft review"}}
		}
		response := (&creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}).call(creditCommand{path: fmt.Sprintf("/invoices/%d%s", invoice, suffix), values: form})
		if response.Code != 403 && response.Code != 404 {
			t.Fatalf("independent Invoice action %s: %d", suffix, response.Code)
		}
	}
	// The retained source is independent of mutable ordinary Invoice lines and
	// Location data; source rows and Customer anchors cannot be rewritten.
	for _, query := range []string{
		`UPDATE lease_finalizations SET terms='[]'`,
		`DELETE FROM lease_invoice_generations`,
		`UPDATE lease_billing_obligations SET financial='{}'`,
		`UPDATE invoices SET lease_id=NULL WHERE lease_id IS NOT NULL`,
	} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatalf("immutable source accepted %s", query)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE locations SET title='Changed later' WHERE id=$1`, l.ID)
	historyCheck(t, err)
	var title string
	historyCheck(t, pool.QueryRow(ctx, `SELECT premises_snapshot->>'title' FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&title))
	if title != "Premises snapshot" {
		t.Fatal("premises snapshot drifted")
	}
	fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), path+"/delete", nil, 409)
	// Replaying is still a currently authorized action.
	var roleRevision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&roleRevision))
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Lease finalizer", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}}, roleRevision)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path+"/finalize", values, 403)
	// Drafts do not reserve Assets, but a second native finalization cannot
	// claim any part of the first agreement's open-ended continuation.
	fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var second int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM leases`).Scan(&second))
	secondPath := fmt.Sprintf("/leases/%d", second)
	fieldPost(t, web, adminCookie, secondPath, url.Values{"revision": {"0"}, "starts_on": {date}, "ends_on": {today.AddDate(2, 0, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, second, 0, date, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &rent, TaxRate: "5"}, 0))
	before = rows()
	fieldPost(t, web, adminCookie, secondPath+"/finalize", values, 409)
	historyEqual(t, "native overlapping coverage rejected atomically", rows(), before)
}

func TestLeaseFinalizationBoundariesHTTP(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		fault                 string
		startDays, cutoffDays int
		exempt                bool
		rate                  string
		want                  int
	}{
		{"incompatible effective rates", "", 0, 0, false, "7", 409},
		{"exempt differing raw rates", "", 0, 0, true, "7", 303},
		{"exact equivalent rates", "", 0, 0, false, "5.000", 303},
		{"future pending", "", 60, 60, false, "0", 303},
		{"advance allowed before cutoff", "", 20, 20, false, "0", 303},
		{"historical cutoff excludes initial", "", -60, 0, false, "0", 303},
		{"missing terms", "terms", 0, 0, true, "7", 409},
		{"archived Asset", "asset", 0, 0, false, "0", 409},
		{"reversed dates", "dates", 0, 0, false, "0", 409},
		{"late initial due", "due", 0, 0, false, "0", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName(tc.name).SetTaxExempt(tc.exempt).SaveX(ctx)
			l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("One").SaveX(ctx)
			b := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Two").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
			fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
			var today time.Time
			historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
			start := today.AddDate(0, 0, tc.startDays).Format(time.DateOnly)
			path := fmt.Sprintf("/leases/%d", id)
			fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {start}, "ends_on": {today.AddDate(1, 0, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID), fmt.Sprint(b.ID)}}, 303)
			svc := authorization.New(pool)
			amount := int64(101)
			for i, asset := range []int64{a.ID, b.ID} {
				rate := "5"
				if i == 1 {
					rate = tc.rate
				}
				historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, int64(i), start, authorization.DraftLeaseTerms{AssetID: asset, InitialCents: &amount, RentCents: &amount, TaxRate: rate}, 0))
			}
			switch tc.fault {
			case "terms":
				_, err := pool.Exec(ctx, `UPDATE lease_draft_asset_terms SET rent_cents=NULL WHERE asset_id=$1`, b.ID)
				historyCheck(t, err)
			case "asset":
				_, err := pool.Exec(ctx, `UPDATE assets SET deleted_at=clock_timestamp() WHERE id=$1`, b.ID)
				historyCheck(t, err)
			case "dates":
				_, err := pool.Exec(ctx, `UPDATE leases SET ends_on=starts_on-1 WHERE id=$1`, id)
				historyCheck(t, err)
			case "due":
				_, err := pool.Exec(ctx, `UPDATE lease_draft_billing SET initial_due_on=initial_due_on+1 WHERE lease_id=$1`, id)
				historyCheck(t, err)
			}
			fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"2"}, "managed_billing_start": {today.AddDate(0, 0, tc.cutoffDays).Format(time.DateOnly)}}, tc.want)
			var finalized, invoices, initial int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_finalizations),(SELECT count(*) FROM invoices),(SELECT count(*) FROM lease_billing_obligations WHERE kind='initial')`).Scan(&finalized, &invoices, &initial))
			if tc.want == 409 {
				if finalized != 0 || invoices != 0 {
					t.Fatal("invalid tax partially finalized")
				}
				return
			}
			if finalized != 1 {
				t.Fatal("no finalization")
			}
			if tc.startDays > 30 && invoices != 0 {
				t.Fatal("premature generation")
			}
			if tc.startDays == 20 && invoices == 0 {
				t.Fatal("cutoff incorrectly blocked advance generation")
			}
			if tc.startDays < 0 && initial != 0 {
				t.Fatal("pre-cutoff charge invented")
			}
			if tc.exempt {
				rows, err := pool.Query(ctx, `SELECT financial FROM invoices`)
				historyCheck(t, err)
				defer rows.Close()
				for rows.Next() {
					var raw string
					historyCheck(t, rows.Scan(&raw))
					snapshot, err := commercial.Read(raw)
					historyCheck(t, err)
					if snapshot.Result.TaxCents != 0 {
						t.Fatal("exemption lost")
					}
				}
				historyCheck(t, rows.Err())
			}
		})
	}
}

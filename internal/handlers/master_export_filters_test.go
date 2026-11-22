package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestMasterExportCollectionFiltersHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	a := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selected Customer").SaveX(ctx)
	b := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
	for _, customer := range []int64{a.ID, a.ID, b.ID} {
		client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(customer).SetFirstName("Repeated name").SetEmail("same@example.test").SetPhone("same phone").SaveX(ctx)
		client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer).SetTitle("Repeated location").SetAddress1("Same address").SaveX(ctx)
	}
	for _, kind := range []string{"contacts", "locations"} {
		collection := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/%s?search=Repeated", a.ID, kind), 200)
		assertContains(t, collection, "/"+kind+"/export?")
		assertContains(t, collection, "customer_id="+fmt.Sprint(a.ID))
		values := url.Values{"search": {"Repeated"}, "customer_id": {fmt.Sprint(a.ID)}, "sort": {"name"}}
		review := requestBody(t, router, cookie, "GET", "/"+kind+"/export?"+values.Encode(), 200)
		assertContains(t, review, "customer_id")
		values.Set("operation_id", newOperationKey())
		path := masterPost(t, router, cookie, "/"+kind+"/export", values, 303)
		if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		body := requestBody(t, router, cookie, "GET", path+"/download", 200)
		rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		if err != nil || len(rows) != 3 {
			t.Fatalf("%s filter/duplicate preservation: %v %v", kind, rows, err)
		}
		if rows[1][0] == rows[2][0] {
			t.Fatalf("%s collapsed duplicate identities", kind)
		}
		assertNotContains(t, body, "Other Customer")
	}
	// The archive collection is archived-only, whereas explicit include-archived
	// export requests can intentionally include both current and archived rows.
	client.Item.Create().SetCompanyID(company).SetName("Current inventory").SetSku("CURRENT").SaveX(ctx)
	old := client.Item.Create().SetCompanyID(company).SetName("Archived inventory").SetSku("RESERVED").SetDeletedAt(time.Now()).SaveX(ctx)
	page := requestBody(t, router, cookie, "GET", "/items?archived=1&search=RESERVED", 200)
	assertContains(t, page, "archived_only=1")
	assertContains(t, page, "search_sku=1")
	for _, route := range []string{"/items/export", "/pricebook/export", "/items/cost/export"} {
		values := url.Values{"archived": {"1"}, "archived_only": {"1"}, "search": {"RESERVED"}, "search_sku": {"1"}, "operation_id": {newOperationKey()}}
		path := masterPost(t, router, cookie, route, values, 303)
		if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		body := requestBody(t, router, cookie, "GET", path+"/download", 200)
		rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		if err != nil || len(rows) != 2 || rows[1][0] != fmt.Sprint(old.ID) {
			t.Fatalf("%s archive selection: %v %v", route, rows, err)
		}
		assertNotContains(t, body, "Current inventory")
	}
	// Invalid and unrecognized filters cannot silently broaden a request.
	for _, query := range []string{"customer_id=-1", "customer_id=invalid", "search=x&search=y", "scope=J", "archived=maybe", "unrecognized_filter=1"} {
		expectStatus(t, router, cookie, "GET", "/contacts/export?"+query, 409)
	}
}

func TestMasterExportAssetFilterAndArchiveHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Placement customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Selected equipment").SaveX(ctx)
	other := client.AssetType.Create().SetCompanyID(company).SetName("Other equipment").SaveX(ctx)
	selected := client.Asset.Create().SetCompanyID(company).SetName("Selected placement").SetCustomerID(customer.ID).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetName("Current equipment").SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetName("Other archived equipment").SetOwnershipClass("company_owned").SetAssetTypeID(other.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}, "archived": {"1"}, "archived_only": {"1"}}
	page := requestBody(t, router, cookie, "GET", "/assets?"+values.Encode(), 200)
	assertContains(t, page, "archived_only=1")
	review := requestBody(t, router, cookie, "GET", "/assets/export?"+values.Encode(), 200)
	assertContains(t, review, "Archived records only")
	values.Set("operation_id", newOperationKey())
	path := masterPost(t, router, cookie, "/assets/export", values, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][0] != fmt.Sprint(selected.ID) {
		t.Fatalf("asset filters: %v %v", rows, err)
	}
	assertContains(t, body, "placement")
	assertNotContains(t, body, "Other archived equipment")
	assertNotContains(t, body, "Current equipment")
}

func TestMasterExportAcceptanceAuditFaultHTTP(t *testing.T) {
	for _, route := range masterExportRoutes {
		t.Run(route.path, func(t *testing.T) {
			pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_master_accept_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='accepted' THEN RAISE EXCEPTION 'injected acceptance audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_master_accept_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_master_accept_audit()`); err != nil {
				t.Fatal(err)
			}
			intent := url.Values{"operation_id": {newOperationKey()}}
			// Permission transactions expose failed commit as a nondisclosing
			// conflict; neither the request nor its audit may survive.
			masterPost(t, router, cookie, route.path, intent, 409)
			var jobs, audits int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM export_jobs),(SELECT count(*) FROM authorization_audit WHERE target_type='export')`).Scan(&jobs, &audits); err != nil || jobs != 0 || audits != 0 {
				t.Fatalf("partial acceptance: jobs=%d audits=%d err=%v", jobs, audits, err)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER fail_master_accept_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			path := masterPost(t, router, cookie, route.path, intent, 303)
			if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			requestBody(t, router, cookie, "GET", path+"/download", 200)
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND action=$1 AND scope='A' AND actor_id=$2 AND detail->>'event' IN ('accepted','generated','downloaded')`, route.permission, admin).Scan(&audits); err != nil || audits != 3 {
				t.Fatalf("audit authority: count=%d err=%v", audits, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE authorization_audit SET action='item.edit' WHERE target_type='export'`); err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("export audit immutability: %v", err)
			}
		})
	}
}

func TestMasterExportOwnTimeAndItemEditingDoNotImplyExportHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "Own time and Item editor", []authorization.Grant{
		{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.export", Scope: authorization.Own},
		{Permission: "item.view", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All},
		{Permission: "pricebook.view", Scope: authorization.All}, {Permission: "pricebook.edit", Scope: authorization.All},
		{Permission: "cost.view", Scope: authorization.All}, {Permission: "cost.edit", Scope: authorization.All},
	})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("own-editor-export@test").SetName("Own editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	for _, route := range masterExportRoutes {
		expectStatus(t, router, cookie, "GET", route.path, 403)
		masterPost(t, router, cookie, route.path, url.Values{"operation_id": {newOperationKey()}}, 403)
	}
	var requests int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("unauthorized requests: %d %v", requests, err)
	}
}

func TestMasterExportPreservesArchivedSKUAndLineSnapshotsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recorded work owner").SaveX(ctx)
	item := client.Item.Create().SetCompanyID(company).SetName("Recorded catalog identity").SetSku("EXPORT-RESERVED").SetUnitPrice(99).SetUnitCost(77).SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Recorded work").SaveX(ctx)
	jobPath := fmt.Sprintf("/jobs/%d", job.ID)
	itemPost(t, router, cookie, jobPath, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(job.ID)}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Accepted capture","unit_price":11.25,"quantity":2}]`, item.ID)}}, 303)
	expectStatus(t, router, cookie, "POST", jobPath+"/create-estimate", 303)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", item.ID), nil, 303)
	tables := []string{"items", "item_sku_claims", "jobs", "estimates"}
	before := historyDurableRows(t, pool, tables)
	for _, path := range []string{"/items/export", "/pricebook/export", "/items/cost/export"} {
		result := masterPost(t, router, cookie, path, url.Values{"operation_id": {newOperationKey()}, "archived": {"1"}}, 303)
		if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		body := requestBody(t, router, cookie, "GET", result+"/download", 200)
		assertContains(t, body, "Recorded catalog identity")
	}
	itemPost(t, router, cookie, "/items", url.Values{"name": {"Forbidden reuse"}, "type": {"service"}, "sku": {"EXPORT-RESERVED"}}, 409)
	historyEqual(t, "export and rejected SKU reuse preserve catalog IDs, SKU reservations and captured Job/document values", historyDurableRows(t, pool, tables), before)
	assertContains(t, client.Job.GetX(ctx, job.ID).LineItems, "11.25")
	assertContains(t, client.Estimate.Query().OnlyX(ctx).LineItems, "11.25")
}

func TestMasterExportAssetAndServiceEntryCursorHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Bulk history owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Bulk equipment").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO assets(company_id,customer_id,ownership_class,asset_type_id,name) SELECT $1,$2,'customer_owned',$3,'Bulk '||lpad((n/2)::text,3,'0') FROM generate_series(0,259) n`, company, customer.ID, typ.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(company_id,customer_id,asset_id,job_type) SELECT company_id,customer_id,id,'Bulk recorded service' FROM assets WHERE company_id=$1 ORDER BY id`, company); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_service_entries(company_id,asset_id,job_id,link_id,customer_id,association_event_id,actor_id) SELECT j.company_id,l.asset_id,j.id,l.id,j.customer_id,l.association_event_id,$2 FROM jobs j JOIN job_asset_links l ON l.job_id=j.id WHERE j.company_id=$1 ORDER BY j.id`, company, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id) SELECT gen_random_uuid(),id,current_date,'Bulk revision '||id,$2 FROM asset_service_entries WHERE company_id=$1 ORDER BY id`, company, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_service_responsible_users(revision_id,user_id) SELECT r.id,$2 FROM asset_service_revisions r JOIN asset_service_entries e ON e.id=r.entry_id WHERE e.company_id=$1`, company, admin); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, sort string }{{"/assets/export", "name_desc"}, {"/service-entries/export", "date"}} {
		path := masterPost(t, router, cookie, tc.path, url.Values{"operation_id": {newOperationKey()}, "sort": {tc.sort}}, 303)
		if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
		if err != nil || len(rows) != 261 {
			t.Fatalf("%s truncated cursor: rows=%d err=%v", tc.path, len(rows), err)
		}
		columns := map[string]int{}
		for i, column := range rows[0] {
			columns[column] = i
		}
		seen := map[string]bool{}
		var prior int64
		for i, row := range rows[1:] {
			if seen[row[0]] {
				t.Fatalf("%s duplicate identity %s", tc.path, row[0])
			}
			seen[row[0]] = true
			if tc.path == "/assets/export" {
				if row[columns["name"]] != fmt.Sprintf("Bulk %03d", 129-i/2) {
					t.Fatalf("Asset sort row%d: %v", i, row)
				}
			} else {
				id, err := strconv.ParseInt(row[0], 10, 64)
				if err != nil || id <= prior {
					t.Fatalf("Service Entry tie-break order: %v", row)
				}
				prior = id
				assertContains(t, row[columns["responsible_users"]], "Admin")
				assertContains(t, row[columns["revisions"]], "Bulk revision")
			}
		}
		var status exports.Status
		masterJSON(t, router, cookie, path, &status)
		if status.RowCount == nil || *status.RowCount != 260 || status.AsOf == nil {
			t.Fatalf("%s snapshot totals: %+v", tc.path, status)
		}
	}
}

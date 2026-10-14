package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestActivityExportFieldReauthorizationAndSeparateGrantHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	base := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Activity reader", base)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Activity exporter").SetEmail("activity-exporter@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Facet export subject").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,metadata) VALUES($1,$2,'customer',$3,'updated','{"before":{"display_name":"Before name","billing_address_1":"Before billing"},"after":{"display_name":"After name","billing_address_1":"After billing"},"password":"SECRET"}')`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	requestBody(t, router, cookie, "GET", "/activity/export", 403)
	masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}}, 403)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/activity", 200), "After billing")
	grants := append(base, authorization.Grant{Permission: "activity.export", Scope: authorization.All}, authorization.Grant{Permission: "customer.financial.view", Scope: authorization.All})
	if _, err := auth.SaveRole(ctx, actor, role, "Renamed independent exporter", grants, 1); err != nil {
		t.Fatal(err)
	}
	path := masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	assertContains(t, body, "After billing")
	assertNotContains(t, body, "SECRET")
	requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", path+"/download", 404)
	if _, err := auth.SaveRole(ctx, actor, role, "Export without financial", grants[:2], 2); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", path+"/download", 410)
	if _, err := auth.SaveRole(ctx, actor, role, "Financial restored", grants, 3); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", path+"/download", 410)
}

func TestActivityExportSnapshotConcurrencyHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Consistent activity subject").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action) SELECT $1,$2,'customer',$3,'updated' FROM generate_series(1,260)`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(102129)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_activity_export() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=129 THEN PERFORM pg_advisory_xact_lock(102129); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_activity_export BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION pause_activity_export()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
	waitExportBlocker(t, ctx, pool, pid)
	// Bypass the product fence deliberately to challenge the export's MVCC snapshot.
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action) VALUES($1,$2,'customer',$3,'created')`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE customers SET display_name='Changed after snapshot' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
	if err != nil || len(rows) != 261 {
		t.Fatalf("snapshot %d %v", len(rows), err)
	}
	for _, row := range rows[1:] {
		if row[8] != "updated" || row[7] != "Consistent activity subject" {
			t.Fatalf("mixed snapshot %v", row)
		}
	}
}

func TestActivityUpgradePreservesFactsAndGrantsHTTP(t *testing.T) {
	migrations := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "074_operational_activity.up.sql" || e.Name() == "081_operational_event_contract.up.sql" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		migrations[e.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, migrations)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained activity identity").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained field source").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_audit(actor_kind,actor_id,role_id,action,scope,target_type,target_id,channel,recorded_at,detail) VALUES('product-user',$1,1,'job.fields.edit','A','job',$2,'web','2024-04-15T12:00:00Z','{"before":"[]","after":"[{\"definition_id\":1,\"value\":\"Original captured value\"}]"}')`, admin, j.ID); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO activity_logs(company_id,object_type,object_id,action,metadata,created_at) VALUES($1,'customer',$2,'updated','{"unreviewed":"DO_NOT_INVENT"}','2025-01-01') RETURNING id`, company, c.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var before, after string
	q := `SELECT jsonb_build_object('grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g),'history',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM activity_logs a WHERE event_key IS NULL))::text`
	if err := pool.QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, q).Scan(&after); err != nil || before != after {
		t.Fatalf("legacy history/grants rewritten: %v", err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=customer&object_id=%d", c.ID), 200)
	assertContains(t, body, "Unknown historical actor")
	assertContains(t, body, "Retained activity identity")
	assertNotContains(t, body, "DO_NOT_INVENT")
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=job&object_id=%d", j.ID), 200)
	assertContains(t, body, "Original captured value")
	assertContains(t, body, "2024")
	assertContains(t, body, "Admin")
	if strings.Count(body, "Original captured value") != 1 {
		t.Fatal("reviewed audit source duplicated")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM activity_logs WHERE id=$1`, id); err == nil {
		t.Fatal("history deletion allowed")
	}
}

func TestActivityConversionProvenanceExportHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Provenance customer").SaveX(ctx)
	fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"Provenance estimate"}, "customer_id": {fmt.Sprint(c.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
	if len(match) != 2 {
		t.Fatal("missing source link")
	}
	source := match[1]
	converted := fieldPost(t, router, cookie, source+"/convert", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	invoiceID := strings.TrimPrefix(invoice, "/invoices/")
	filters := url.Values{"object_type": {"invoice"}, "object_id": {invoiceID}, "conversion": {"1"}}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action) VALUES($1,$2,'invoice',$3,'updated')`, company, admin, invoiceID); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/activity?"+filters.Encode(), 200)
	assertContains(t, body, "provenance only")
	assertNotContains(t, body, `href="`+source+`"`)
	requestBody(t, router, cookie, "GET", "/activity/export?"+filters.Encode(), 200)
	filters.Set("operation_id", newOperationKey())
	path := masterPost(t, router, cookie, "/activity/export", filters, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "provenance only")
	fieldPost(t, router, cookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	// The snapshot includes the then-visible Invoice, now a tombstone: no subset download.
	requestBody(t, router, cookie, "GET", path+"/download", 410)
}

func TestActivityExportFailureRestartExpiryHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Durable activity artifact").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action) VALUES($1,$2,'customer',$3,'updated')`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	accept := func() string {
		return masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	}
	failed := accept()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_activity_generation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='activity.export' AND NEW.detail->>'event'='generated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_activity_generation_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_activity_generation_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
		t.Fatal("generation audit failure was acknowledged")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", failed, 200), "Failed")
	requestBody(t, router, cookie, "GET", failed+"/download", 409)
	var bytes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_records`).Scan(&bytes); err != nil || bytes != 0 {
		t.Fatalf("partial artifact persisted: %d %v", bytes, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_activity_generation_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	ready := accept()
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET state='Running' WHERE state='Pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if work, err := exports.Product(pool).ProcessOne(ctx); err != nil || work {
		t.Fatalf("restarted completed export duplicated: %v %v", work, err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", ready+"/download", 200), "Durable activity artifact")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_activity_download_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='activity.export' AND NEW.detail->>'event'='downloaded' THEN RAISE EXCEPTION 'injected download audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_activity_download_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_activity_download_audit()`); err != nil {
		t.Fatal(err)
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", ready+"/download", 503), "Durable activity artifact")
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_activity_download_audit ON authorization_audit; UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE state='Ready'`); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", ready+"/download", 410)
}

func TestActivityDocumentDetailsHistoryAtomicHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Document history customer").SaveX(ctx)
	input := url.Values{"title": {"Original proposal title"}, "customer_id": {fmt.Sprint(c.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}
	fieldPost(t, router, cookie, "/estimates", input, 303)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
	if len(match) != 2 {
		t.Fatal("missing estimate")
	}
	path := match[1]
	input.Set("title", "Revised proposal title")
	fieldPost(t, router, cookie, path, input, 303)
	body := requestBody(t, router, cookie, "GET", path+"/activity", 200)
	assertContains(t, body, "Original proposal title")
	assertContains(t, body, "Revised proposal title")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_details_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='updated' AND NEW.object_type='estimate' THEN RAISE EXCEPTION 'history storage unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_details_history BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_details_history()`); err != nil {
		t.Fatal(err)
	}
	input.Set("title", "Rejected proposal title")
	fieldPost(t, router, cookie, path, input, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Revised proposal title")
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), "Rejected proposal title")
}

func TestActivityExportCommitTimeGrantLossHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Commit-time exporter", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "activity.export", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Queued exporter").SetEmail("queued-activity@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='activity.export'`, role); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/activity/export", strings.NewReader(url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	waitExportBlocker(t, ctx, pool, pid)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 403 {
		t.Fatalf("stale export grant accepted: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs WHERE requester_id=$1`, u.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected action persisted export: %d %v", count, err)
	}
}

func TestActivityContactHistoryUsesIndependentViewHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Contact history only", []authorization.Grant{{Permission: "contact.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Contact reader").SetEmail("contact-history@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE CUSTOMER CONTEXT").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Visible contact").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action) VALUES($1,$2,'contact',$3,'updated')`, company, admin, contact.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=contact&object_id=%d", contact.ID), 200)
	assertContains(t, body, "Visible contact")
	assertNotContains(t, body, "PRIVATE CUSTOMER CONTEXT")
	requestBody(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=customer&object_id=%d", c.ID), 403)
}

func TestActivityFacetOnlyChangesDoNotAffectPaginationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Operational history", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Operational reader").SetEmail("facet-history@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Visible activity customer").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,metadata,created_at) VALUES($1,$2,'customer',$3,'updated','{"before":{"service_notes":"Known operational before"},"after":{"service_notes":"Known operational after"}}','2025-01-01')`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,metadata) SELECT $1,$2,'customer',$3,'updated','{"before":{"billing_address_1":"Hidden financial before"},"after":{"billing_address_1":"Hidden financial after"}}'::jsonb FROM generate_series(1,30)`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, u.ID), "GET", "/activity?type=customer", 200)
	assertContains(t, body, "Known operational after")
	assertNotContains(t, body, "Hidden financial")
	assertNotContains(t, body, "Older</a>")
	if strings.Count(body, `class="activity-item activity-item-compact"`) != 1 {
		t.Fatal("inaccessible facet affected visible rows")
	}
}

func TestActivityRecordedAuthorshipSurvivesRenameHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Authorship subject"}, "account_type": {"individual"}}, 303)
	if _, err := pool.Exec(ctx, `UPDATE users SET name='Current display name' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	path := masterPost(t, router, cookie, "/activity/export", url.Values{"type": {"customer"}, "action": {"created"}, "operation_id": {newOperationKey()}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("history rows: %d %v", len(rows), err)
	}
	if rows[1][4] != "Admin" {
		t.Fatalf("historic author relabelled: %q", rows[1][4])
	}
}

func TestActivityItemFacetSnapshotsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	itemPost(t, router, adminCookie, "/items", url.Values{"name": {"Item history subject"}, "type": {"service"}, "unit_price": {"50"}, "unit_cost": {"9876.54"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM items WHERE name='Item history subject'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, adminCookie, fmt.Sprintf("/items/%d", id), url.Values{"unit_cost": {"12345.67"}}, 303)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Base Item history", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Item reader").SetEmail("item-history@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", "/activity?type=item", 200)
	assertContains(t, body, "Item history subject")
	assertNotContains(t, body, "9876.54")
	assertNotContains(t, body, "12345.67")
	assertNotContains(t, body, "pricebook_created")
	if strings.Count(body, `class="activity-item activity-item-compact"`) != 1 {
		t.Fatal("Item facets leaked event counts")
	}
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Cost history", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "cost.view", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", "/activity?type=item", 200)
	assertContains(t, body, "9876.54")
	assertContains(t, body, "12345.67")
}

func TestActivityServiceEntryHistoryRequiresCurrentOperationalAssetHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Service history customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Service equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Service asset").SetOwnershipClass("customer_owned").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Service history job").SetAssetID(a.ID).SaveX(ctx)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Assigned Service history", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}, {Permission: "activity.export", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Service reader").SetEmail("service-history@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,metadata) VALUES($1,$2,'job',$3,'service_entry_revised',jsonb_build_object('asset_id',$4::bigint,'before',jsonb_build_object('summary','Original service summary','service_date','2020-01-01'),'after',jsonb_build_object('summary','Revised service summary','service_date','2020-01-02','responsible_names',jsonb_build_array('Responsible technician'))))`, company, admin, j.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", "/activity", 200)
	assertContains(t, body, "Revised service summary")
	assertContains(t, body, "Responsible technician")
	assertContains(t, body, "2020-01-02")
	path := masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	client.Job.UpdateOneID(j.ID).ClearAssetID().SaveX(ctx)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/activity", 200), "Revised service summary")
	requestBody(t, router, cookie, "GET", path+"/download", 410)
}

func TestActivityFinancialFactsAndReversalAuthorityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Financial history customer").SaveX(ctx)
	var issued int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced' LIMIT 1`, company).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(102).SetTitle("Financial history invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100}]`).SaveX(ctx)
	input := url.Values{"amount": {"25.00"}, "method": {"cash"}, "date": {time.Now().Format("2006-01-02")}, "idempotency_key": {newOperationKey()}}
	itemPost(t, router, adminCookie, fmt.Sprintf("/invoices/%d/payments", i.ID), input, 303)
	itemPost(t, router, adminCookie, fmt.Sprintf("/invoices/%d/payments", i.ID), input, 303)
	var payment string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM invoice_payments WHERE invoice_id=$1`, i.ID).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, adminCookie, fmt.Sprintf("/invoices/%d/payments/%s/reverse", i.ID, payment), url.Values{"effective_date": {time.Now().Format("2006-01-02")}, "reason": {"Entered twice"}, "idempotency_key": {newOperationKey()}}, 303)
	auth := authorization.New(pool)
	base := []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Invoice history reader", base)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Invoice reader").SetEmail("financial-history@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/activity?object_type=invoice&object_id=%d", i.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "No activity recorded yet.")
	grants := append(base, authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "customer.financial.view", Scope: authorization.All}, authorization.Grant{Permission: "settlement.view", Scope: authorization.All})
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Settlement history reader", grants, 1); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "amount_cents")
	assertContains(t, body, "2500")
	assertContains(t, body, "Entered twice")
	assertContains(t, body, payment)
	if strings.Count(body, `class="activity-item activity-item-compact"`) != 2 {
		t.Fatal("financial replay duplicated history")
	}
}

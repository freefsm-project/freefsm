package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/justinas/nosurf"
)

var masterExportRoutes = []struct {
	path, permission string
	views            []string
}{
	{"/contacts/export", "contact.export", []string{"contact.view"}},
	{"/locations/export", "location.export", []string{"location.view"}},
	{"/assets/export", "asset.export", []string{"asset.view"}},
	{"/projects/export", "project.export", []string{"project.view"}},
	{"/service-entries/export", "service_entry.export", []string{"service_entry.view", "asset.view"}},
	{"/items/export", "item.export", []string{"item.view"}},
	{"/pricebook/export", "pricebook.export", []string{"item.view", "pricebook.view"}},
	{"/items/cost/export", "cost.export", []string{"item.view", "cost.view"}},
}

// Empty results still require the complete contract: having no rows must not
// turn an incompatible View scope or a missing explicit Export into authority.
func TestMasterExportsIndependentAuthorityHTTP(t *testing.T) {
	for _, route := range masterExportRoutes {
		t.Run(route.path, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			grants := []authorization.Grant{}
			for _, view := range route.views {
				grants = append(grants, authorization.Grant{Permission: view, Scope: authorization.All})
			}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent exporter", grants)
			if err != nil {
				t.Fatal(err)
			}
			user := client.User.Create().SetCompanyID(company).SetEmail("masterexport@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, user.ID)
			values := func() url.Values { return url.Values{"operation_id": {newOperationKey()}} }
			expectStatus(t, router, cookie, "GET", route.path, 403)
			masterPost(t, router, cookie, route.path, values(), 403)
			if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,$2,'A')`, role, route.permission); err != nil {
				t.Fatal(err)
			}
			assertContains(t, requestBody(t, router, cookie, "GET", route.path, 200), "All matching rows")
			for _, sibling := range masterExportRoutes {
				if sibling.path != route.path {
					expectStatus(t, router, cookie, "GET", sibling.path, 403)
				}
			}
			intent := values()
			path := masterPost(t, router, cookie, route.path, intent, 303)
			if replay := masterPost(t, router, cookie, route.path, intent, 303); replay != path {
				t.Fatalf("replay %s != %s", replay, path)
			}
			worker := exports.Product(pool)
			if _, err := worker.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
			if err != nil || len(rows) != 1 {
				t.Fatalf("empty CSV: %v %v", rows, err)
			}
			other := sessionCookie(t, ctx, sessions, admin)
			expectStatus(t, router, other, "GET", path, 404)
			expectStatus(t, router, other, "GET", path+"/download", 404)
			// Each prerequisite is independently required, including Item base View
			// beneath Cost/Pricebook. Restore does not revive a blocked artifact.
			for _, view := range route.views {
				path = masterPost(t, router, cookie, route.path, values(), 303)
				if _, err := worker.ProcessOne(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, view); err != nil {
					t.Fatal(err)
				}
				expectStatus(t, router, cookie, "GET", route.path, 403)
				masterPost(t, router, cookie, route.path, values(), 403)
				expectStatus(t, router, cookie, "GET", path+"/download", 410)
				var status exports.Status
				masterJSON(t, router, cookie, path, &status)
				if status.RowCount != nil || status.AsOf != nil || !status.Blocked {
					t.Fatalf("restricted metadata: %+v", status)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,$2,'A')`, role, view); err != nil {
					t.Fatal(err)
				}
				expectStatus(t, router, cookie, "GET", path+"/download", 410)
			}
			// Export A cannot be paired with assigned/context-only View, even
			// when the result has no rows and no assignments are needed to scan it.
			if route.permission != "item.export" && route.permission != "pricebook.export" && route.permission != "cost.export" {
				view := route.views[0]
				scope := "C"
				if route.permission == "service_entry.export" {
					scope = "J"
				}
				if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2;`, role, view); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,$2,$3),($1,'job.view','J')`, role, view, scope); err != nil {
					t.Fatal(err)
				}
				expectStatus(t, router, cookie, "GET", route.path, 403)
				masterPost(t, router, cookie, route.path, values(), 403)
				if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, view); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,$2,'A')`, role, view); err != nil {
					t.Fatal(err)
				}
			}
			path = masterPost(t, router, cookie, route.path, values(), 303)
			if _, err := worker.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/")); err != nil {
				t.Fatal(err)
			}
			expectStatus(t, router, cookie, "GET", path+"/download", 410)
			// A queued request must see the grant edit committed while it waits.
			queuedChildPost(t, pool, router, cookie, route.path, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, []any{role, route.permission}, 403, values())
			// These browser-only exports never become public field API endpoints.
			token, err := sessions.CreateMobile(ctx, admin, "Master exports excluded")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(pool, client, sessions)
			var before, after int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for _, apiPath := range []string{route.path, "/exports", path, path + "/download"} {
				for _, method := range []string{"GET", "POST"} {
					r := httptest.NewRequest(method, apiPath, strings.NewReader(values().Encode()))
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					if w.Code != 404 && w.Code != 405 {
						t.Fatalf("field API exposed %s %s: %d", method, apiPath, w.Code)
					}
				}
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("field API created exports: before=%d after=%d", before, after)
			}
		})
	}
}

func TestMasterExportsAuditAtomicityHTTP(t *testing.T) {
	for _, route := range masterExportRoutes {
		t.Run(route.path, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fault fixture customer").SaveX(ctx)
			const marker = "Populated artifact must roll back"
			switch route.path {
			case "/contacts/export":
				client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetFirstName(marker).SaveX(ctx)
			case "/locations/export":
				client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle(marker).SaveX(ctx)
			case "/projects/export":
				client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName(marker).SaveX(ctx)
			case "/assets/export", "/service-entries/export":
				typ := client.AssetType.Create().SetCompanyID(company).SetName("Fault fixture equipment").SaveX(ctx)
				asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetOwnershipClass("customer_owned").SetName(marker).SetAssetTypeID(typ.ID).SaveX(ctx)
				if route.path == "/service-entries/export" {
					job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetAssetID(asset.ID).SetJobType("Fault fixture service Job").SaveX(ctx)
					var entry int64
					if err := pool.QueryRow(ctx, `INSERT INTO asset_service_entries(company_id,asset_id,job_id,link_id,customer_id,association_event_id,actor_id) SELECT $1,$2,$3,id,$4,association_event_id,$5 FROM job_asset_links WHERE job_id=$3 RETURNING id`, company, asset.ID, job.ID, customer.ID, admin).Scan(&entry); err != nil {
						t.Fatal(err)
					}
					const revision = "97000000-0000-4000-8000-000000000093"
					if _, err := pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id) VALUES($1,$2,current_date,$3,$4)`, revision, entry, marker, admin); err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(ctx, `INSERT INTO asset_service_responsible_users(revision_id,user_id) VALUES($1,$2)`, revision, admin); err != nil {
						t.Fatal(err)
					}
				}
			default:
				client.Item.Create().SetCompanyID(company).SetName(marker).SetSku("FAULT-SKU").SetUnitPrice(876.54).SetUnitCost(123.45).SaveX(ctx)
			}
			cookie := sessionCookie(t, ctx, sessions, admin)
			create := func() string {
				return masterPost(t, router, cookie, route.path, url.Values{"operation_id": {newOperationKey()}}, 303)
			}
			path := create()
			// A sequence is deliberately nontransactional evidence: advance it
			// only if the audit transaction can see an already-written data row.
			// The injected exception must then roll back that row and publication.
			if _, err := pool.Exec(ctx, `CREATE SEQUENCE master_export_populated_fault;
CREATE FUNCTION reject_master_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='generated' THEN IF EXISTS(SELECT 1 FROM export_records WHERE ordinal>0) THEN PERFORM nextval('master_export_populated_fault'); END IF; RAISE EXCEPTION 'injected generation audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_master_export_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_master_export_audit()`); err != nil {
				t.Fatal(err)
			}
			worker := exports.Product(pool)
			if _, err := worker.ProcessOne(ctx); err == nil {
				t.Fatal("generation ignored audit fault")
			}
			var populatedBeforeFault bool
			if err := pool.QueryRow(ctx, `SELECT is_called FROM master_export_populated_fault`).Scan(&populatedBeforeFault); err != nil {
				t.Fatal(err)
			}
			if !populatedBeforeFault {
				t.Fatal("audit fault did not observe a written domain data row")
			}
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
			expectStatus(t, router, cookie, "GET", path+"/download", 409)
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_records`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial artifact: %d %v", count, err)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_master_export_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			path = create()
			// Competing replacement workers must publish precisely one generation.
			var wg sync.WaitGroup
			errors := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := exports.Product(pool).ProcessOne(ctx); errors <- err }()
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("generation audit: %d %v", count, err)
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_master_download_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='downloaded' THEN RAISE EXCEPTION 'injected download audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_master_download_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_master_download_audit()`); err != nil {
				t.Fatal(err)
			}
			assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/download", 503), marker)
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='downloaded'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed download audit: %d %v", count, err)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_master_download_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			body := requestBody(t, router, cookie, "GET", path+"/download", 200)
			assertContains(t, body, marker)
			rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
			if err != nil || len(rows) != 2 {
				t.Fatalf("retry did not publish exactly one domain row: %d %v", len(rows), err)
			}
		})
	}
}

func TestMasterExportsItemSnapshotFacetsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for i := 0; i < 260; i++ {
		client.Item.Create().SetCompanyID(company).SetName(fmt.Sprintf("Snapshot %03d", i/2)).SetSku(fmt.Sprintf("SKU-%03d", i)).SetUnitPrice(876.54).SetUnitCost(123.45).SaveX(ctx)
	}
	for _, route := range masterExportRoutes[5:] {
		path := masterPost(t, router, cookie, route.path, url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
		if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		body := requestBody(t, router, cookie, "GET", path+"/download", 200)
		rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		if err != nil || len(rows) != 261 {
			t.Fatalf("%s truncated snapshot: %d %v", route.path, len(rows), err)
		}
		nameIndex := -1
		for i, h := range rows[0] {
			if strings.EqualFold(h, "name") || strings.EqualFold(h, "item_name") {
				nameIndex = i
			}
		}
		if nameIndex < 0 {
			t.Fatalf("missing name column: %v", rows[0])
		}
		for i, row := range rows[1:] {
			if row[nameIndex] != fmt.Sprintf("Snapshot %03d", i/2) {
				t.Fatalf("%s row %d order: %v", route.path, i, row)
			}
		}
		if route.permission != "cost.export" {
			assertNotContains(t, body, "123.45")
		}
		if route.permission == "item.export" {
			assertNotContains(t, body, "876.54")
		}
		if route.permission == "pricebook.export" {
			assertContains(t, body, "876.54")
		}
		if route.permission == "cost.export" {
			assertContains(t, body, "123.45")
		}
		var status exports.Status
		masterJSON(t, router, cookie, path, &status)
		if status.RowCount == nil || *status.RowCount != 260 || status.AsOf == nil {
			t.Fatalf("snapshot totals: %+v", status)
		}
	}
}

func TestMasterExportsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser export parent").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name,email) VALUES($1,$2,'Browser duplicate contact','duplicate@example.test'),($1,$2,'Browser duplicate contact','duplicate@example.test')`, company, customer.ID); err != nil {
		t.Fatal(err)
	}
	for _, sku := range []string{"BROWSER-SKU-A", "BROWSER-SKU-B"} {
		client.Item.Create().SetCompanyID(company).SetName("Browser duplicate item").SetSku(sku).SetUnitPrice(876.54).SetUnitCost(123.45).SaveX(ctx)
	}
	archived := client.Item.Create().SetCompanyID(company).SetName("Selected archived browser item").SetSku("BROWSER-RESERVED").SetUnitPrice(876.54).SetUnitCost(123.45).SetDeletedAt(time.Now()).SaveX(ctx)
	client.Item.Create().SetCompanyID(company).SetName("Excluded archived browser item").SetSku("UNRELATED-RESERVED").SetDeletedAt(time.Now()).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	browserCtx, stop := context.WithTimeout(ctx, 180*time.Second)
	defer stop()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/master-exports.cjs", server.URL, cookie.Value, fmt.Sprint(archived.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMasterExportsItemConcurrentSnapshotHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mutateSource := exportSnapshotWriter(t, ctx, pool)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `INSERT INTO items(company_id,name) SELECT $1,'Original '||lpad(n::text,4,'0') FROM generate_series(1,260) n`, company); err != nil {
		t.Fatal(err)
	}
	path := masterPost(t, router, cookie, "/items/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(97129)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_master_export_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=129 THEN PERFORM pg_advisory_xact_lock(97129); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_master_export_record BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION pause_master_export_record()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
	waitExportBlocker(t, ctx, pool, pid)
	mutateSource(pid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE items SET name='Changed during cursor'; INSERT INTO items(name) VALUES('Inserted during cursor')`)
		return err
	})
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 261 {
		t.Fatalf("mixed snapshot: %d %v", len(rows), err)
	}
	for i, row := range rows[1:] {
		found := false
		for _, value := range row {
			if value == fmt.Sprintf("Original %04d", i+1) {
				found = true
			}
		}
		if !found {
			t.Fatalf("snapshot row %d: %v", i, row)
		}
	}
	assertNotContains(t, body, "Changed during cursor")
	assertNotContains(t, body, "Inserted during cursor")
}

func TestMasterExportsItemWholeArtifactRecheckHTTP(t *testing.T) {
	for _, route := range masterExportRoutes[5:] {
		t.Run(route.path, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			grants := []authorization.Grant{{Permission: route.permission, Scope: authorization.All}}
			for _, view := range route.views {
				grants = append(grants, authorization.Grant{Permission: view, Scope: authorization.All})
			}
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Artifact requester", grants)
			if err != nil {
				t.Fatal(err)
			}
			user := client.User.Create().SetCompanyID(company).SetEmail("artifact@test").SetName("Requester").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, user.ID)
			client.Item.Create().SetCompanyID(company).SetName("A visible sibling").SaveX(ctx)
			item := client.Item.Create().SetCompanyID(company).SetName("Z restricted sibling").SaveX(ctx)
			path := masterPost(t, router, cookie, route.path, url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
			if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			body := requestBody(t, router, cookie, "GET", path+"/download", 200)
			assertContains(t, body, "A visible sibling")
			assertContains(t, body, "Z restricted sibling")
			if strings.Index(body, "A visible sibling") >= strings.Index(body, "Z restricted sibling") {
				t.Fatal("fixture must recheck an authorized row before the restricted row")
			}
			client.Item.UpdateOneID(item.ID).SetDeletedAt(time.Now()).SaveX(ctx)
			body = requestBody(t, router, cookie, "GET", path+"/download", 410)
			assertNotContains(t, body, "A visible sibling")
			assertNotContains(t, body, "Z restricted sibling")
			client.Item.UpdateOneID(item.ID).ClearDeletedAt().SaveX(ctx)
			expectStatus(t, router, cookie, "GET", path+"/download", 410)
		})
	}
}

func TestMasterExportsRelationshipsSortedHTTP(t *testing.T) {
	for _, tc := range []struct{ path, sql, column string }{
		{"/contacts/export", `INSERT INTO customer_contacts(company_id,customer_id,first_name,email,phone) SELECT $1,$2,'Duplicate '||lpad((n/2)::text,3,'0'),'same@example.test','555-0100' FROM generate_series(0,259) n`, "first_name"},
		{"/locations/export", `INSERT INTO locations(company_id,object_type,object_id,title,address_1) SELECT $1,'customer',$2,'Duplicate '||lpad((n/2)::text,3,'0'),'Same address' FROM generate_series(0,259) n`, "title"},
		{"/projects/export", `INSERT INTO projects(company_id,customer_id,name,completion_percentage) SELECT $1,$2,'Duplicate '||lpad((n/2)::text,3,'0'),37 FROM generate_series(0,259) n`, "name"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Immutable parent").SaveX(ctx)
			if _, err := pool.Exec(ctx, tc.sql, company, customer.ID); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, admin)
			path := masterPost(t, router, cookie, tc.path, url.Values{"operation_id": {newOperationKey()}, "sort": {"name_desc"}}, 303)
			if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			body := requestBody(t, router, cookie, "GET", path+"/download", 200)
			rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
			if err != nil || len(rows) != 261 {
				t.Fatalf("all rows: %d %v", len(rows), err)
			}
			columns := map[string]int{}
			for i, column := range rows[0] {
				columns[column] = i
			}
			for _, column := range []string{tc.column, "customer_id", "customer_label"} {
				if _, ok := columns[column]; !ok {
					t.Fatalf("missing column %s", column)
				}
			}
			seen := map[string]bool{}
			for i, row := range rows[1:] {
				if seen[row[0]] {
					t.Fatalf("duplicate stable identity %s", row[0])
				}
				seen[row[0]] = true
				if row[columns[tc.column]] != fmt.Sprintf("Duplicate %03d", 129-i/2) {
					t.Fatalf("sort row %d: %v", i, row)
				}
				if row[columns["customer_id"]] != fmt.Sprint(customer.ID) || row[columns["customer_label"]] != "Immutable parent" {
					t.Fatalf("parent relationship: %v", row)
				}
				if tc.column != "name" {
					if row[columns["primary_customer_default"]] != "false" {
						t.Fatalf("invented primary default: %v", row)
					}
					if row[columns["job_links_json"]] != "[]" {
						t.Fatalf("invented Job links: %v", row)
					}
				}
			}
		})
	}
}

// Recreate the pre-durable-export schema using the actual embedded migration
// set. Installing the lifecycle must not rewrite masters or upgrade Role grants.
func TestMasterExportsPriorSchemaPreservationHTTP(t *testing.T) {
	migrations := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "072_durable_exports.up.sql" {
			continue
		}
		body, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		migrations[entry.Name()] = &fstest.MapFile{Data: body}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, migrations)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Preserved export parent").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name,email) VALUES($1,$2,'Preserved duplicate','same@test'),($1,$2,'Preserved duplicate','same@test')`, company, customer.ID); err != nil {
		t.Fatal(err)
	}
	client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Preserved location").SetAddress1("Recorded address").SaveX(ctx)
	client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Preserved project").SetCompletionPercentage(37).SaveX(ctx)
	client.Item.Create().SetCompanyID(company).SetName("Preserved item").SetSku("RESERVED-ARCHIVED").SetUnitPrice(876.54).SetUnitCost(123.45).SetDeletedAt(time.Now()).SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Preserved equipment type").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetOwnershipClass("customer_owned").SetName("Preserved numbered asset").SetAssetTypeID(typ.ID).SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetAssetID(asset.ID).SetJobType("Preserved service Job").SaveX(ctx)
	person := client.User.Create().SetCompanyID(company).SetEmail("preserved-service@test").SetName("Recorded responsible person").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	var entry int64
	if err := pool.QueryRow(ctx, `INSERT INTO asset_service_entries(company_id,asset_id,job_id,link_id,customer_id,association_event_id,actor_id) SELECT $1,$2,$3,id,$4,association_event_id,$5 FROM job_asset_links WHERE job_id=$3 RETURNING id`, company, asset.ID, job.ID, customer.ID, admin).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	first, second := "97000000-0000-4000-8000-000000000091", "97000000-0000-4000-8000-000000000092"
	if _, err := pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id) VALUES($1,$2,current_date-1,'Preserved original summary',$3)`, first, entry, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id,predecessor) VALUES($1,$2,current_date,'Preserved revised summary',$3,$4)`, second, entry, admin, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_service_responsible_users(revision_id,user_id) VALUES($1,$3),($2,$3)`, first, second, person.ID); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Existing nonprotected role", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}}); err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{
		"contacts":  `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM customer_contacts t`,
		"locations": `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM locations t`,
		"projects":  `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM projects t`,
		"items":     `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM items t`,
		"grants":    `SELECT jsonb_agg(to_jsonb(t) ORDER BY role_id,permission,scope)::text FROM authorization_grants t`,
	}
	for _, table := range []string{"assets", "asset_types", "asset_association_origins", "asset_association_events", "asset_association_origin_revisions", "job_asset_links", "jobs", "asset_service_entries", "asset_service_revisions", "asset_service_responsible_users", "users"} {
		queries[table] = `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + table + ` t`
	}
	before := map[string]string{}
	for key, query := range queries {
		var snapshot string
		if err := pool.QueryRow(ctx, query).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		before[key] = snapshot
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	for key, query := range queries {
		var snapshot string
		if err := pool.QueryRow(ctx, query).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot != before[key] {
			t.Fatalf("upgrade rewrote %s", key)
		}
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, route := range masterExportRoutes {
		path := masterPost(t, router, cookie, route.path, url.Values{"operation_id": {newOperationKey()}}, 303)
		if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		body := requestBody(t, router, cookie, "GET", path+"/download", 200)
		if route.path == "/assets/export" {
			assertContains(t, body, "Preserved numbered asset")
		}
		if route.path == "/service-entries/export" {
			assertContains(t, body, "Preserved revised summary")
			assertContains(t, body, "Recorded responsible person")
			assertContains(t, body, second)
		}
	}
}

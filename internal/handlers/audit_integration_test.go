package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
)

func TestProtectedAuditBrowseHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Audit customer"}, "account_type": {"individual"}}, 303)
	body := requestBody(t, router, cookie, "GET", "/settings/audit?permission=customer.create&scope=A&channel=web&outcome=success&role=Administrator", 200)
	assertContains(t, body, "customer.create")
	assertContains(t, body, "Administrator")
	assertContains(t, body, "Admin")
	assertNotContains(t, body, "Audit customer") // Raw input/detail is not the protected evidence projection.
	evidence := requestBody(t, router, cookie, "GET", "/settings/audit?permission=audit.view", 200)
	assertContains(t, evidence, "audit.view")
}

func TestProtectedAuditUnknownMetadataCannotInventOccurrenceHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel,detail) VALUES($1,1,'fixture.untrusted','A','job',103,'api','{"occurred_at":"now","operation_id":"credential-is-not-operation","session_id":"not-a-session","device_name":{"password":"secret"}}')`, admin); err != nil {
		t.Fatal(err)
	}
	path := masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}, "permission": {"fixture.untrusted"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][19] != "" || rows[1][20] != "" || rows[1][21] != "" || rows[1][22] != "" {
		t.Fatalf("unrecorded metadata became facts: %#v", rows)
	}
	assertNotContains(t, body, "secret")
}

func TestProtectedAuditAPIKnownSessionFactsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API evidence context").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("API evidence Job").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Recorded field"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Field event device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), strings.NewReader(`{"operation_id":"10300000-0000-4000-8000-000000000003","revisions":{"1":"00000000-0000-0000-0000-000000000000"},"values":[{"definition_id":1,"value":"Sensitive field value"}]}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("field API: %d %s", w.Code, w.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET device_name='Current field device' WHERE kind='mobile'`); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/settings/audit?permission=job.fields.edit&channel=api", 200)
	assertContains(t, body, "Field event device")
	assertContains(t, body, "10300000-0000-4000-8000-000000000003")
	assertNotContains(t, body, "Current field device")
	assertNotContains(t, body, "Sensitive field value")
}

func TestProtectedAuditPendingReviewAndOrdinaryBrowsingHTTP(t *testing.T) {
	t.Run("unreviewed legacy installation", func(t *testing.T) {
		pool, _, router, sessions, _, admin := permissionHTTPFixture(t, true)
		cookie := sessionCookie(t, context.Background(), sessions, admin)
		expectStatus(t, router, cookie, "GET", "/settings/audit", 503)
		masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}}, 503)
		var ready bool
		if err := pool.QueryRow(context.Background(), `SELECT ready FROM authorization_state`).Scan(&ready); err != nil || ready {
			t.Fatalf("unreviewed cutover changed: %v %v", ready, err)
		}
	})
	t.Run("ordinary navigation is not audit access", func(t *testing.T) {
		pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
		ctx := context.Background()
		cookie := sessionCookie(t, ctx, sessions, admin)
		requestBody(t, router, cookie, "GET", "/settings/audit", 200)
		var before, after int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/customers", "/search?q=audit.view", "/activity"} {
			body := requestBody(t, router, cookie, "GET", path, 200)
			assertNotContains(t, body, "event_role_identity")
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("ordinary browsing mutated audit: %d -> %d", before, after)
		}
	})
}

func TestProtectedAuditConsistentAllRowsAndConcurrentReplayHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel,detail) SELECT $1,1,'snapshot.fixture','A','customer',n,'web','{"secret":"omitted"}' FROM generate_series(1,260) n`, admin); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"operation_id": {newOperationKey()}, "permission": {"snapshot.fixture"}, "sort": {"oldest"}, "per_page": {"25"}}
	path := masterPost(t, router, cookie, "/settings/audit/export", values, 303)
	replies := make(chan string, 3)
	for range 3 {
		go func() {
			r := httptest.NewRequest("POST", "/settings/audit/export", strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			replies <- fmt.Sprintf("%d:%s", w.Code, w.Header().Get("Location"))
		}()
	}
	for range 3 {
		if replay := <-replies; replay != "303:"+path {
			t.Fatalf("concurrent replay: %s", replay)
		}
	}
	changed := url.Values{"operation_id": values["operation_id"], "permission": {"snapshot.fixture"}, "sort": {"newest"}}
	masterPost(t, router, cookie, "/settings/audit/export", changed, 409)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(103129)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_audit_export() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=129 THEN PERFORM pg_advisory_xact_lock(103129); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_audit_export BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION pause_audit_export()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
	waitExportBlocker(t, ctx, pool, pid)
	assertContains(t, requestBody(t, router, cookie, "GET", "/exports", 200), "Running")
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel) VALUES($1,1,'snapshot.fixture','A','customer',999,'web')`, admin); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 261 {
		t.Fatalf("truncated/mixed snapshot: %d", len(rows))
	}
	for i, row := range rows[1:] {
		if row[12] != fmt.Sprint(i+1) {
			t.Fatalf("sort/snapshot %d: %s", i, row[12])
		}
	}
	assertNotContains(t, body, "omitted")
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if status.RowCount == nil || *status.RowCount != 260 || status.AsOf == nil {
		t.Fatalf("inconsistent totals: %+v", status)
	}
	page := requestBody(t, router, cookie, "GET", "/settings/audit?permission=snapshot.fixture&per_page=25&sort=oldest", 200)
	assertContains(t, page, "Next page")
	if strings.Count(page, "<td>snapshot.fixture</td>") != 25 {
		t.Fatal("browser paging ignored")
	}
}

func TestProtectedAuditGenerationDownloadFailuresAndRestartHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	create := func() string {
		return masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	}
	path := create()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_audit_generation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='audit.export' AND NEW.detail->>'event'='generated' THEN RAISE EXCEPTION 'generation audit failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_audit_generation BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_audit_generation()`); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
		t.Fatal("generation failure reported success")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
	expectStatus(t, router, cookie, "GET", path+"/download", 409)
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_audit_generation ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	path = create()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_audit_storage() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal>0 THEN RAISE EXCEPTION 'storage failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_audit_storage BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION fail_audit_storage()`); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
		t.Fatal("storage failure reported success")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_records`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial publication: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_audit_storage ON export_records`); err != nil {
		t.Fatal(err)
	}
	path = create()
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_audit_download() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='audit.export' AND NEW.detail->>'event'='downloaded' THEN RAISE EXCEPTION 'download audit failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_audit_download BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_audit_download()`); err != nil {
		t.Fatal(err)
	}
	denied := requestBody(t, router, cookie, "GET", path+"/download", 503)
	assertNotContains(t, denied, "audit_id,accepted_at")
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_audit_download ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", path+"/download", 200)
	path = create()
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET state='Running' WHERE id=$1`, strings.TrimPrefix(path, "/exports/")); err != nil {
		t.Fatal(err)
	}
	// Fresh process recovers a durable claim, sharing the production registry.
	command := exec.Command(os.Args[0], "-test.run=^TestProtectedAuditWorkerProcess$", "-test.v")
	command.Env = append(os.Environ(), "FREEFSM_AUDIT_WORKER_PROBE="+pool.Config().ConnConfig.ConnString())
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replacement process: %s %v", out, err)
	}
	requestBody(t, router, cookie, "GET", path+"/download", 200)
}

func TestProtectedAuditWorkerProcess(t *testing.T) {
	dsn := os.Getenv("FREEFSM_AUDIT_WORKER_PROBE")
	if dsn == "" {
		t.Skip("subprocess helper exercised by restart test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := exports.Product(pool).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedAuditPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Browser audit fixture"}}, 303)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	browserCtx, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/protected-audit.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProtectedAuditUpgradePreservesUnknownAndRecordedFactsHTTP(t *testing.T) {
	migrations := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "074_protected_audit_evidence.up.sql" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		migrations[entry.Name()] = &fstest.MapFile{Data: b}
	}
	pool, _, router, sessions, _, admin := permissionHTTPFixtureMigrations(t, migrations)
	ctx := context.Background()
	// Recorded pre-upgrade evidence, including a colliding actor ID, is immutable.
	_, err = pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel,detail) VALUES($1,1,'legacy.known','A','job',103,'api','{"device_name":"Historical device","operation_id":"10300000-0000-4000-8000-000000000001","occurred_at":"2025-01-02T03:04:05Z","password":"never-visible","request_body":"never-visible"}')`, admin)
	if err != nil {
		t.Fatal(err)
	}
	var grantsBefore, grantsAfter string
	query := `SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope)::text FROM authorization_grants g`
	if err := pool.QueryRow(ctx, query).Scan(&grantsBefore); err != nil {
		t.Fatal(err)
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, query).Scan(&grantsAfter); err != nil {
		t.Fatal(err)
	}
	if grantsBefore != grantsAfter {
		t.Fatal("upgrade changed existing grants")
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}, "permission": {"legacy.known"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][3] != fmt.Sprint(admin) || rows[1][4] != "" || rows[1][5] != "" || rows[1][8] != "" {
		t.Fatalf("invented historical identity: %#v", rows)
	}
	if rows[1][19] != "Historical device" || rows[1][21] != "10300000-0000-4000-8000-000000000001" || rows[1][22] != "2025-01-02T03:04:05Z" {
		t.Fatalf("lost recorded API facts: %#v", rows)
	}
	assertNotContains(t, body, "never-visible")
	for _, sql := range []string{`UPDATE authorization_audit SET actor_id=999`, `DELETE FROM authorization_audit`, `TRUNCATE authorization_audit`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("mutable protected audit")
		}
	}
}

func TestProtectedAuditSealedDenialsAndAuthorityLossHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "Former Administrator", []authorization.Grant{{Permission: "directory.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.export", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("audit-denied@test").SetName("Ordinary exporter").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	expectStatus(t, router, cookie, "GET", "/directory", 200)
	for _, path := range []string{"/settings/audit", "/settings/audit/export"} {
		expectStatus(t, router, cookie, "GET", path, 403)
	}
	masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}}, 403)
	if _, err := auth.SaveRole(ctx, actor, 0, "Sealed impersonator", []authorization.Grant{{Permission: "audit.view", Scope: authorization.Sealed}, {Permission: "audit.export", Scope: authorization.Sealed}}); err == nil {
		t.Fatal("sealed grants delegated")
	}
	// A second real Administrator cannot download another requester's artifact.
	if err := auth.AssignUser(ctx, actor, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	path := masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	expectStatus(t, router, cookie, "GET", path+"/download", 409)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, adminCookie, "GET", path+"/download", 404)
	requestBody(t, router, cookie, "GET", path+"/download", 200)
	if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if !status.Blocked || status.RowCount != nil || status.AsOf != nil {
		t.Fatalf("lost authority leaked metadata: %+v", status)
	}
	if err := auth.AssignUser(ctx, actor, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	path = masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/")); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	// All audit routes remain outside the field API even for protected Admin.
	token, err := sessions.CreateMobile(ctx, admin, "Audit exclusion")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, path := range []string{"/settings/audit", "/settings/audit/export", "/exports", "/exports/1/download"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("API exposed audit %s: %d", path, w.Code)
		}
	}
}

func TestProtectedAuditEventRoleAndAPIDeviceSnapshotsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "Event-time operator", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("audit-author@test").SetName("Original actor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Role snapshot"}}, 303)
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, actor, role, "Renamed operator", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}, revision); err != nil {
		t.Fatal(err)
	}
	client.User.UpdateOneID(user.ID).SetName("Current actor name").SaveX(ctx)
	body := requestBody(t, router, adminCookie, "GET", "/settings/audit?permission=customer.create&role=Event-time+operator", 200)
	assertContains(t, body, "Original actor")
	assertNotContains(t, body, "Current actor name")
	assertNotContains(t, body, "Renamed operator")
	assertContains(t, requestBody(t, router, adminCookie, "GET", "/settings/audit?permission=customer.create&role=Renamed+operator", 200), "No matching audit evidence")
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API context").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Audit API").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Recorded mobile device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/comments", j.ID), strings.NewReader(`{"operation_id":"10300000-0000-4000-8000-000000000002","content":"Never emit comment content","occurred_at":"2025-01-02T03:04:05Z"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("API comment: %d %s", w.Code, w.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET device_name='Renamed mobile device' WHERE kind='mobile'`); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, adminCookie, "GET", "/settings/audit?permission=comment.add&channel=api", 200)
	assertContains(t, body, "Recorded mobile device")
	assertContains(t, body, "10300000-0000-4000-8000-000000000002")
	assertContains(t, body, "2025-01-02T03:04:05Z")
	assertNotContains(t, body, "Renamed mobile device")
	assertNotContains(t, body, "Never emit comment content")
}

func TestProtectedAuditFailureAndCommitTimeAuthorityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Unrelated product browsing"}}, 303)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_audit_access() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('audit.view','audit.export') THEN RAISE EXCEPTION 'injected audit access failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_audit_access BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_audit_access()`); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/settings/audit?permission=customer.create", 503)
	assertNotContains(t, body, "<td>customer.create</td>")
	masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}}, 409)
	requestBody(t, router, cookie, "GET", "/customers", 200)
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_audit_access ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/settings/audit?permission=audit.export", 200), "No matching audit evidence")
	user := client.User.Create().SetCompanyID(company).SetEmail("audit-race@test").SetName("Revoked audit requester").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, 1); err != nil {
		t.Fatal(err)
	}
	other := sessionCookie(t, ctx, sessions, user.ID)
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
	if _, err := blocker.Exec(ctx, `UPDATE users SET authorization_role_id=2 WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("GET", "/settings/audit?permission=customer.create", nil)
		r.AddCookie(other)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w.Code
	}()
	waitExportBlocker(t, ctx, pool, pid)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 403 {
		t.Fatalf("stale audit authority: %d", code)
	}
}

func TestProtectedAuditRestoreIdentityAndArtifactsHTTP(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_BACKUP_TEST_ADMIN_URL to owned disposable cluster")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Audit source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Audit destination")
	makeRouter := func(f *historyBackupInstance) http.Handler {
		return middleware.Company(services.NewCompanySettingsService(f.client))(New(f.pool, f.client, f.sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20}))
	}
	sourceRouter := makeRouter(src)
	sourceCookie := &http.Cookie{Name: "session", Value: src.web}
	fieldPost(t, sourceRouter, sourceCookie, "/customers", url.Values{"display_name": {"Audit restore input"}}, 303)
	path := masterPost(t, sourceRouter, sourceCookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}, "permission": {"customer.create"}}, 303)
	if _, err := exports.Product(src.pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	before := requestBody(t, sourceRouter, sourceCookie, "GET", path+"/download", 200)
	src.client.User.UpdateOneID(src.actor).SetName("Source current name").SaveX(ctx)
	dst.client.User.UpdateOneID(dst.actor).SetName("Colliding destination user").SaveX(ctx)
	if src.actor != dst.actor {
		t.Fatal("fixture must challenge identical numeric IDs")
	}
	src.roundTripTo(t, dst)
	destinationRouter := makeRouter(dst)
	// Both previous sessions are revoked by real restore. New restored-account
	// authentication is required before prepared evidence can be downloaded.
	expectStatus(t, destinationRouter, &http.Cookie{Name: "session", Value: dst.web}, "GET", path+"/download", 303)
	expectStatus(t, destinationRouter, sourceCookie, "GET", path+"/download", 303)
	fresh := sessionCookie(t, ctx, dst.sessions, src.actor)
	after := requestBody(t, destinationRouter, fresh, "GET", path+"/download", 200)
	if before != after {
		t.Fatal("restore rewrote prepared immutable audit evidence")
	}
	assertNotContains(t, after, "Colliding destination user")
	assertContains(t, after, "Historical administrator")
	body := requestBody(t, destinationRouter, fresh, "GET", "/settings/audit?permission=customer.create", 200)
	assertContains(t, body, "Historical administrator")
	assertNotContains(t, body, "Colliding destination user")
	assertNotContains(t, body, "<td>Source current name</td>")
	// Application log rotation is outside the database and cannot mutate history.
	log := t.TempDir() + "/application.log"
	previousLogger := slog.Default()
	defer slog.SetDefault(previousLogger)
	for i := range 2 {
		file, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		slog.SetDefault(slog.New(slog.NewJSONHandler(file, nil)))
		expectStatus(t, destinationRouter, fresh, "GET", "/exports/999999/download", 404)
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := os.Rename(log, log+".1"); err != nil {
				t.Fatal(err)
			}
		}
	}
	slog.SetDefault(previousLogger)
	for _, path := range []string{log, log + ".1"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		assertContains(t, string(data), "authorization request rejected")
	}
	if got := requestBody(t, destinationRouter, fresh, "GET", path+"/download", 200); got != before {
		t.Fatal("log rotation changed evidence")
	}
	if _, err := dst.pool.Exec(ctx, `UPDATE authorization_audit SET role_name='rewritten'`); err == nil {
		t.Fatal("restored history is mutable")
	}
}

func TestProtectedAuditExportHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Never export raw customer input"}}, 303)
	review := requestBody(t, router, cookie, "GET", "/settings/audit/export?permission=customer.create&sort=oldest", 200)
	assertContains(t, review, "All matching rows")
	path := masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}, "permission": {"customer.create"}, "sort": {"oldest"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Pending")
	if _, err := exports.Product(pool).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][9] != "customer.create" || rows[1][8] != "Administrator" {
		t.Fatalf("unexpected evidence: %#v", rows)
	}
	assertNotContains(t, body, "Never export raw customer input")
	evidence := requestBody(t, router, cookie, "GET", "/settings/audit?permission=audit.export&channel=worker", 200)
	assertContains(t, evidence, "system")
	assertContains(t, evidence, "Admin")
}

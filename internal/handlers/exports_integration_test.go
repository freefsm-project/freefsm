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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
)

func TestCustomerExportDurableAcceptanceHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	review := requestBody(t, router, cookie, "GET", "/customers/export?search=Needle&sort=name_desc", 200)
	assertContains(t, review, "Needle")
	assertContains(t, review, "All matching rows")
	values := url.Values{"operation_id": {newOperationKey()}, "search": {"Needle"}, "sort": {"name_desc"}}
	path := masterPost(t, router, cookie, "/customers/export", values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Pending")
	replay := masterPost(t, router, cookie, "/customers/export", values, 303)
	if replay != path {
		t.Fatalf("accepted export duplicated: %s != %s", replay, path)
	}
	values.Set("search", "Changed")
	masterPost(t, router, cookie, "/customers/export", values, 409)
}

func TestCustomerExportUpgradeAndFieldAPIExclusion(t *testing.T) {
	migrations := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "072_durable_exports.up.sql" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		migrations[entry.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, migrations)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recorded identifier survives").SetServiceNotes("Historical service fact").SaveX(ctx)
	var before, after string
	query := `SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope)::text FROM authorization_grants g`
	if err := pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, query).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("upgrade altered existing grants")
	}
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", customer.ID), 200)
	assertContains(t, body, "Recorded identifier survives")
	assertContains(t, body, "Historical service fact")
	token, err := sessions.CreateMobile(ctx, admin, "Exports excluded")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, path := range []string{"/customers/export", "/exports", "/exports/1/download"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("field API exposed exports %s: %d", path, w.Code)
		}
	}
}

func TestCustomerExportSnapshotConcurrentEditHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mutateSource := exportSnapshotWriter(t, ctx, pool)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `INSERT INTO customers(company_id,display_name) SELECT $1,'Original '||lpad(n::text,4,'0') FROM generate_series(1,260) n`, company); err != nil {
		t.Fatal(err)
	}
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(96129)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_export_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=129 THEN PERFORM pg_advisory_xact_lock(96129); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_export_record BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION pause_export_record()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := exports.New(pool, exports.Customers{}).ProcessOne(ctx); done <- err }()
	waitExportBlocker(t, ctx, pool, pid)
	// Half the cursor has been consumed. The fixture administrator bypasses the
	// SQL writer fence locally; runtime writers must still wait on the exporter.
	mutateSource(pid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE customers SET display_name='New value'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Inserted later')`, company)
		return err
	})
	var changed, inserted int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE display_name='New value'),count(*) FILTER (WHERE display_name='Inserted later') FROM customers WHERE company_id=$1`, company).Scan(&changed, &inserted); err != nil {
		t.Fatal(err)
	}
	if changed != 260 || inserted != 1 {
		t.Fatalf("concurrent source edit was not applied: changed=%d inserted=%d", changed, inserted)
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
		t.Fatalf("mixed count: %d", len(rows))
	}
	for i, row := range rows[1:] {
		if row[1] != fmt.Sprintf("Original %04d", i+1) {
			t.Fatalf("mixed snapshot row %d: %q", i, row[1])
		}
	}
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if status.RowCount == nil || *status.RowCount != 260 {
		t.Fatalf("mixed totals: %+v", status)
	}
}

func waitExportBlocker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pid int) {
	t.Helper()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCustomerExportWorkerProcess(t *testing.T) {
	dsn := os.Getenv("FREEFSM_EXPORT_WORKER_PROBE")
	if dsn == "" {
		t.Skip("subprocess helper; exercised by crash recovery parent")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := exports.New(pool, exports.Customers{}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCustomerExportProcessCrashRecoveryHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	client.Customer.Create().SetCompanyID(company).SetDisplayName("Survives worker crash").SaveX(ctx)
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(96130)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_export_crash() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(96130); RETURN NEW; END $$; CREATE TRIGGER pause_export_crash BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION pause_export_crash()`); err != nil {
		t.Fatal(err)
	}
	command := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCustomerExportWorkerProcess$", "-test.v")
		cmd.Env = append(os.Environ(), "FREEFSM_EXPORT_WORKER_PROBE="+pool.Config().ConnConfig.ConnString())
		return cmd
	}
	first := command()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if first.ProcessState == nil {
			_ = first.Process.Kill()
			_ = first.Wait()
		}
	}()
	waitExportBlocker(t, ctx, pool, pid)
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Running")
	if out, err := command().CombinedOutput(); err != nil {
		t.Fatalf("replacement worker: %s %v", out, err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "Survives worker crash")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("crash duplicated generation %d %v", count, err)
	}
}

func TestCustomerExportAuthorityExpiryAndFailureHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.export", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Export clerk", grants)
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("export@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private snapshot row").SaveX(ctx)
	create := func() string {
		return masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	}
	worker := exports.New(pool, exports.Customers{})
	run := func() {
		t.Helper()
		if _, err := worker.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	path := create()
	expectStatus(t, router, cookie, "GET", path+"/download", 409)
	run()
	expectStatus(t, router, adminCookie, "GET", path, 404)
	expectStatus(t, router, adminCookie, "GET", path+"/download", 404)
	requestBody(t, router, cookie, "GET", path+"/download", 200)
	// One row becoming inaccessible blocks the entire snapshot permanently.
	client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	denied := requestBody(t, router, cookie, "GET", path+"/download", 410)
	assertNotContains(t, denied, "Private")
	client.Customer.UpdateOneID(c.ID).ClearDeletedAt().SaveX(ctx)
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	path = create()
	run()
	id := strings.TrimPrefix(path, "/exports/")
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Generate a new export")
	path = create()
	run()
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.export'`, role); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/customers/export", 403)
	masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}}, 403)
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if status.RowCount != nil || status.AsOf != nil || !status.Blocked {
		t.Fatalf("denied metadata exposed: %+v", status)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'customer.export','A')`, role); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	// Generation audit failure rolls back bytes and Ready publication.
	path = create()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='generated' THEN RAISE EXCEPTION 'injected generation audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_export_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_export_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOne(ctx); err == nil {
		t.Fatal("audit fault succeeded")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
	expectStatus(t, router, cookie, "GET", path+"/download", 409)
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_export_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	path = create()
	run()
	// Filesystem transfer preparation failure returns no CSV or success audit.
	t.Setenv("TMPDIR", "/nonexistent-freefsm-export-test")
	expectStatus(t, router, cookie, "GET", path+"/download", 503)
}

func TestCustomerExportRestartConcurrencyAndEmptyHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	values := url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}
	const n = 8
	paths := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/customers/export", strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 303 {
				paths <- fmt.Sprintf("error:%d", w.Code)
			} else {
				paths <- w.Header().Get("Location")
			}
		}()
	}
	wg.Wait()
	close(paths)
	path := ""
	for p := range paths {
		if path == "" {
			path = p
		}
		if p != path || strings.HasPrefix(p, "error") {
			t.Fatalf("duplicate concurrent intent: %s / %s", p, path)
		}
	}
	// Simulate a worker dying after its durable claim, before publication. A
	// replacement service must recover without another accepted logical export.
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET state='Running'`); err != nil {
		t.Fatal(err)
	}
	failures := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := exports.New(pool, exports.Customers{}).ProcessOne(ctx)
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "No matching rows")
	csvBody := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(csvBody)).ReadAll()
	if err != nil || len(rows) != 1 {
		t.Fatalf("empty result: %q %v", csvBody, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate generation %d %v", count, err)
	}
}

func TestCustomerExportPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser CSV Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.New(pool, exports.Customers{}).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	browserCtx, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/customer-exports.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCustomerExportSnapshotHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for i := 0; i < 105; i++ {
		client.Customer.Create().SetCompanyID(company).SetDisplayName(fmt.Sprintf("Duplicate %03d", i/2)).SaveX(ctx)
	}
	original := "Comma, quote\" and\nUnicode 雪"
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName(original).SetServiceNotes("=SUM(A1:A2)").SaveX(ctx)
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}, "per_page": {"25"}}, 303)
	worker := exports.New(pool, exports.Customers{})
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Ready")
	client.Customer.UpdateOneID(c.ID).SetDisplayName("Changed after snapshot").SaveX(ctx)
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 107 {
		t.Fatalf("got %d CSV lines", len(records))
	}
	if records[1][1] != original || records[1][8] != "'=SUM(A1:A2)" {
		t.Fatalf("snapshot/CSV escaping: %#v", records[1])
	}
	assertNotContains(t, body, "Changed after snapshot")
	assertNotContains(t, body, "credit")
	assertNotContains(t, body, "billing")
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if status.RowCount == nil || *status.RowCount != 106 || status.AsOf == nil || status.ReadyAt == nil {
		t.Fatalf("snapshot metadata missing: %+v", status)
	}
}

func TestCustomerExportSearchFacetsAndCommitRecheckHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.export", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Facet export", grants)
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("facetexport@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Relationship only").SaveX(ctx)
	masterPost(t, router, adminCookie, fmt.Sprintf("/customers/%d/contacts", c.ID), url.Values{"first_name": {"Needle Contact"}}, 303)
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "search": {"needle"}, "sort": {"name_desc"}}, 303)
	worker := exports.Product(pool)
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "Relationship only")
	// Losing the field authority that determined membership blocks even though
	// operational Customer View and Export themselves remain granted.
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='contact.view'`, role); err != nil {
		t.Fatal(err)
	}
	status := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, status, "unavailable")
	assertNotContains(t, status, "Completed:")
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'contact.view','A')`, role); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	// A request which passed browser discovery waits behind a grant edit and
	// must authorize with the committed grants, never its request-start cache.
	queuedChildPost(t, pool, router, cookie, "/customers/export", `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.export'`, []any{role}, 403, url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}})
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'customer.export','A')`, role); err != nil {
		t.Fatal(err)
	}
	path = masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	// Export without its compatible View is ineffective, including for workers.
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOne(ctx); err == nil {
		t.Fatal("worker generated without compatible View")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
	expectStatus(t, router, cookie, "GET", "/customers/export", 403)
}

func TestCustomerExportStorageAndAuditFailuresHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	client.Customer.Create().SetCompanyID(company).SetDisplayName("Never partial").SaveX(ctx)
	create := func() string {
		return masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	}
	path := create()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_export_storage() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal>0 THEN RAISE EXCEPTION 'injected storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_export_storage BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION fail_export_storage()`); err != nil {
		t.Fatal(err)
	}
	worker := exports.Product(pool)
	if _, err := worker.ProcessOne(ctx); err == nil {
		t.Fatal("storage failure accepted")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_records`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial artifact retained: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_export_storage ON export_records`); err != nil {
		t.Fatal(err)
	}
	path = create()
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_download_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='downloaded' THEN RAISE EXCEPTION 'injected download audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_download_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_download_audit()`); err != nil {
		t.Fatal(err)
	}
	denied := requestBody(t, router, cookie, "GET", path+"/download", 503)
	assertNotContains(t, denied, "Never partial")
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_download_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		requestBody(t, router, cookie, "GET", path+"/download", 200)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='downloaded'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("successful downloads not audited: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_records SET csv='corrupted'::bytea WHERE export_id=$1 AND ordinal=1`, strings.TrimPrefix(path, "/exports/")); err != nil {
		t.Fatal(err)
	}
	denied = requestBody(t, router, cookie, "GET", path+"/download", 503)
	assertNotContains(t, denied, "corrupted")
}

func TestCustomerExportCSVValuesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	values := []string{"=1+1", "+SUM(A1)", "-42", "@SUM(A1)", " \t=CMD()", "'literal", "normal", "line\nbreak", "雪, \"quoted\""}
	expected := []string{"'=1+1", "'+SUM(A1)", "'-42", "'@SUM(A1)", "' \t=CMD()", "''literal", "normal", "line\nbreak", "雪, \"quoted\""}
	for _, v := range values {
		client.Customer.Create().SetCompanyID(company).SetDisplayName("Duplicate").SetServiceNotes(v).SaveX(ctx)
	}
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(expected)+1 {
		t.Fatalf("wrong row count %d", len(rows))
	}
	for i, want := range expected {
		if rows[i+1][8] != want {
			t.Fatalf("value/order %d: %q want %q", i, rows[i+1][8], want)
		}
	}
}

func TestCustomerExportEmptySnapshotRechecksContractHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.export", Scope: authorization.All}, {Permission: "customer.archived.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Archived exporter", grants)
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("archivedexport@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}, "archived": {"1"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.archived.view'`, role); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if status.RowCount != nil {
		t.Fatal("empty snapshot disclosed lost archived facet")
	}
}

func TestCustomerExportExpiryDuringTransferPreparationHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := masterPost(t, router, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}, "sort": {"name"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	// Audit is part of preparation and can be slow. Crossing expiry during it
	// must roll back that audit and send no bytes, even for an empty result.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION slow_export_download() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='downloaded' THEN PERFORM pg_sleep(1.2); END IF; RETURN NEW; END $$; CREATE TRIGGER slow_export_download BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION slow_export_download()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours'+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='downloaded'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired preparation audited as success: %d %v", count, err)
	}
}

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/justinas/nosurf"
)

func TestVocabularyProductionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Vocabulary manager", []authorization.Grant{{Permission: "configuration.tags.view", Scope: authorization.Configuration}, {Permission: "configuration.tags.manage", Scope: authorization.Configuration}, {Permission: "configuration.assets.view", Scope: authorization.Configuration}, {Permission: "configuration.assets.manage", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("vocabulary@test").SetName("Vocabulary").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, router, cookie, "GET", "/tags", 200)
	expectStatus(t, router, cookie, "POST", "/tags?name=Priority", 303)
	expectStatus(t, router, cookie, "POST", "/tags?name=+PRIORITY+", 409)
	expectStatus(t, router, cookie, "POST", "/tags?name=%C3%89quipement", 303)
	expectStatus(t, router, cookie, "POST", "/tags?name=%C2%A0%C3%A9QUIPEMENT%C2%A0", 409)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM tags WHERE name='Priority'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/tags/%d/retire", id), 303)
	expectStatus(t, router, cookie, "POST", "/tags?name=priority", 409)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/tags/%d/reactivate", id), 303)
	expectStatus(t, router, cookie, "POST", "/settings/asset-types?name=Custom", 303)
	expectStatus(t, router, cookie, "POST", "/settings/asset-types?name=+CUSTOM+", 409)
	expectStatus(t, router, cookie, "POST", "/settings/asset-statuses?name=Custom&color=%23112233", 303)
	expectStatus(t, router, cookie, "POST", "/settings/asset-statuses?name=+CUSTOM+", 409)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Hidden parent").SaveX(ctx)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/customers/%d/tags/%d/attach", c.ID, id), http.StatusNotFound)
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='tag' AND target_id=$1`, id).Scan(&events); err != nil || events != 3 {
		t.Fatalf("events=%d err=%v", events, err)
	}
	expectStatus(t, router, cookie, "GET", "/tags/activity", 200)
	body := requestBody(t, router, cookie, "GET", "/tags/history", 200)
	assertContains(t, body, "Priority")
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Vocabulary view only", []authorization.Grant{{Permission: "configuration.tags.view", Scope: authorization.Configuration}}, 1); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", "/tags", 200)
	assertNotContains(t, body, "New Tag")
	assertNotContains(t, body, "/retire")
	expectStatus(t, router, cookie, "POST", "/tags?name=Forbidden", 403)
	expectStatus(t, router, cookie, "GET", "/tags/new", 403)
	expectStatus(t, router, cookie, "GET", "/settings/assets", 403)
	expectStatus(t, router, cookie, "POST", "/settings/asset-types?name=Forbidden", 403)
	// Broad product authority never expands the public field API.
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Vocabulary exclusion")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/tags", "/settings/asset-types", "/settings/asset-statuses", fmt.Sprintf("/customers/%d/tags/%d/attach", c.ID, id)} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("field API expanded %s: %d", path, w.Code)
		}
	}
}

func TestVocabularyPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "configuration.tags.view", Scope: authorization.Configuration}, {Permission: "configuration.tags.manage", Scope: authorization.Configuration}, {Permission: "configuration.assets.view", Scope: authorization.Configuration}, {Permission: "configuration.assets.manage", Scope: authorization.Configuration}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "tag.attach", Scope: authorization.All}, {Permission: "tag.detach", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser vocabulary", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Browser").SetEmail("browser-vocabulary@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser parent").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/vocabulary.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	var name, color string
	var retired bool
	if err := pool.QueryRow(ctx, `SELECT name,color,retired FROM tags`).Scan(&name, &color, &retired); err != nil || name != "Browser renamed" || color != "#EF4444" || retired {
		t.Fatalf("tag %q %q %v: %v", name, color, retired, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tag_links`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("links %d: %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT retired FROM asset_types WHERE name='Browser type'`).Scan(&retired); err != nil || !retired {
		t.Fatalf("type retired=%v %v", retired, err)
	}
}

func TestVocabularyReferencesAndAtomicAuditHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("References").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Referenced Type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Descriptive").SaveX(ctx)
	a := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SetName("Asset").SaveX(ctx)
	tag := client.Tag.Create().SetCompanyID(company).SetName("Attached").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Job").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Project").SaveX(ctx)
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Estimate").SaveX(ctx)
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(2801).SetTitle("Invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	expectStatus(t, router, nil, "POST", fmt.Sprintf("/invoices/%d/tags/%d/attach", invoice.ID, tag.ID), 401)
	expectStatus(t, router, cookie, "POST", "/invoices/not-an-id/tags/1/attach", 400)
	var otherCompany int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other','vocabulary-other') RETURNING id`).Scan(&otherCompany); err != nil {
		t.Fatal(err)
	}
	foreign := client.Tag.Create().SetCompanyID(otherCompany).SetName("Foreign").SaveX(ctx)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/tags/%d/attach", invoice.ID, foreign.ID), 404)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/tags/%d?name=", foreign.ID), 404)
	for _, parent := range []struct {
		kind string
		id   int64
	}{{"customer", c.ID}, {"asset", a.ID}, {"job", job.ID}, {"project", project.ID}, {"estimate", estimate.ID}, {"invoice", invoice.ID}} {
		path := fmt.Sprintf("/%ss/%d/tags/%d/", parent.kind, parent.id, tag.ID)
		expectStatus(t, router, cookie, "POST", path+"attach", 200)
		expectStatus(t, router, cookie, "POST", path+"attach", 409)
		expectStatus(t, router, cookie, "POST", path+"detach", 200)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/customers/%d/tags/%d/attach", c.ID, tag.ID), 200)
	for _, v := range []struct {
		prefix string
		id     int64
	}{{"/tags", tag.ID}, {"/settings/asset-types", typ.ID}, {"/settings/asset-statuses", status.ID}} {
		expectStatus(t, router, cookie, "POST", fmt.Sprintf("%s/%d/retire", v.prefix, v.id), 303)
		expectStatus(t, router, cookie, "POST", fmt.Sprintf("%s/%d/delete", v.prefix, v.id), 409)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/attach", job.ID, tag.ID), 409)
	// An unchanged retired required Type remains legal on a real details save.
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/assets/%d?name=Unrelated+edit&customer_id=%d&asset_type_id=%d&asset_status_id=%d", a.ID, c.ID, typ.ID, status.ID), 303)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/assets?name=New&customer_id=%d&asset_type_id=%d", c.ID, typ.ID), 409)
	// Replacement requires an active definition, but never reactivates the old one.
	replacement := client.AssetType.Create().SetCompanyID(company).SetName("Replacement").SaveX(ctx)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/assets/%d?name=Unrelated+edit&customer_id=%d&asset_type_id=%d&asset_status_id=%d", a.ID, c.ID, replacement.ID, status.ID), 303)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/assets/%d?name=Must+rollback&customer_id=%d&asset_type_id=%d", a.ID, c.ID, typ.ID), 409)
	var currentType, currentOwner int64
	var currentName string
	var retired bool
	if err := pool.QueryRow(ctx, `SELECT asset_type_id,customer_id,name FROM assets WHERE id=$1`, a.ID).Scan(&currentType, &currentOwner, &currentName); err != nil || currentType != replacement.ID || currentOwner != c.ID || currentName != "Unrelated edit" {
		t.Fatalf("invalid replacement partially applied: %d %d %q %v", currentType, currentOwner, currentName, err)
	}
	if err := pool.QueryRow(ctx, `SELECT retired FROM asset_types WHERE id=$1`, typ.ID).Scan(&retired); err != nil || !retired {
		t.Fatalf("silent reactivation %t %v", retired, err)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/settings/asset-statuses/%d?name=Renamed+description&color=%%23EF4444", status.ID), 303)
	bodyStatus := requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200)
	assertContains(t, bodyStatus, "Renamed description")
	// Deleting an unused retired definition retains immutable history and IDs.
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/settings/asset-types/%d/delete", typ.ID), 303)
	var history int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='asset_type' AND target_id=$1`, typ.ID).Scan(&history); err != nil || history != 2 {
		t.Fatalf("deleted definition history %d %v", history, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE assets SET deleted_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/customers/%d/tags/%d/detach", c.ID, tag.ID), 403)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/settings/asset-statuses/%d/delete", status.ID), 409)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/tags/%d/delete", tag.ID), 409)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 200)
	assertContains(t, body, "Attached")
	assertContains(t, body, "retired")
	// Failure of either history sink rejects the definition transaction.
	auditTag := client.Tag.Create().SetCompanyID(company).SetName("Audit failure reference").SaveX(ctx)
	for _, table := range []string{"authorization_audit", "activity_logs"} {
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_vocabulary_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected history failure'; END $$; CREATE TRIGGER reject_vocabulary_audit BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_vocabulary_audit()`); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, router, cookie, "POST", "/tags?name=Must+rollback", 500)
		// The same transaction guarantee applies to parent references.
		expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/attach", job.ID, auditTag.ID), 500)
		var links int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, auditTag.ID).Scan(&links); err != nil || links != 0 {
			t.Fatalf("partial reference %d %v", links, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM tags WHERE name='Must rollback'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial definition %d %v", count, err)
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER reject_vocabulary_audit ON `+table+`; DROP FUNCTION reject_vocabulary_audit()`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE authorization_audit SET detail='{}' WHERE target_type='tag'`); err == nil {
		t.Fatal("immutable history was rewritten")
	}
}

func TestVocabularyCommitTimeRevocationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Revocable vocabulary", []authorization.Grant{{Permission: "configuration.tags.view", Scope: authorization.Configuration}, {Permission: "configuration.tags.manage", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Revoked").SetEmail("revoked-vocabulary@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("POST", "/tags?name=Waiting+write", nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		done <- w.Code
	}()
	// Observe the actual waiting writer, rather than guessing request timing.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%190019%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer did not wait for authority lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='configuration.tags.manage'`, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 403 {
		t.Fatalf("revoked waiting write status %d", code)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tags WHERE name='Waiting write'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked effect %d %v", count, err)
	}
}

func TestAssetVocabularyReferenceRaces(t *testing.T) {
	pool, client, _, _, company, _ := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
	for _, operation := range []string{"DELETE FROM asset_types WHERE id=$1", "UPDATE asset_types SET retired=true WHERE id=$1"} {
		typ := client.AssetType.Create().SetCompanyID(company).SetName(fmt.Sprintf("Race %d", time.Now().UnixNano())).SaveX(ctx)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO assets(ownership_class,company_id,customer_id,asset_type_id,name) VALUES('customer_owned',$1,$2,$3,'Concurrent reference')`, company, c.ID, typ.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := pool.Exec(ctx, operation, typ.ID); done <- err }()
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		err = <-done
		if operation[0] == 'D' && err == nil {
			t.Fatal("deletion orphaned asset")
		}
		if operation[0] == 'U' && err != nil {
			t.Fatal(err)
		}
		if operation[0] == 'U' {
			if _, err := pool.Exec(ctx, `INSERT INTO assets(ownership_class,company_id,customer_id,asset_type_id,name) VALUES('customer_owned',$1,$2,$3,'New retired selection')`, company, c.ID, typ.ID); err == nil {
				t.Fatal("retirement admitted new selection")
			}
		}
	}
	// Deletion wins: the blocked insert fails its real FK, with no orphan.
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Delete first").SaveX(ctx)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM asset_types WHERE id=$1`, typ.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `INSERT INTO assets(ownership_class,company_id,customer_id,asset_type_id,name) VALUES('customer_owned',$1,$2,$3,'Must reject')`, company, c.ID, typ.ID)
		done <- err
	}()
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("deleted type selected")
	}
}

func TestVocabularyConcurrentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	run := func(paths ...string) []int {
		t.Helper()
		start := make(chan struct{})
		codes := make([]int, len(paths))
		var wg sync.WaitGroup
		for i, path := range paths {
			wg.Add(1)
			go func(i int, path string) {
				defer wg.Done()
				<-start
				r := httptest.NewRequest("POST", path, nil)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				codes[i] = w.Code
			}(i, path)
		}
		close(start)
		wg.Wait()
		return codes
	}
	one := func(codes []int, success int) {
		t.Helper()
		n := 0
		for _, code := range codes {
			if code == success {
				n++
			} else if code != 409 && code != 404 {
				t.Fatalf("unexpected race statuses %v", codes)
			}
		}
		if n != 1 {
			t.Fatalf("race statuses %v", codes)
		}
	}
	for _, prefix := range []string{"/tags", "/settings/asset-types", "/settings/asset-statuses"} {
		one(run(prefix+"?name=Race", prefix+"?name=+RACE+"), 303)
		one(run(prefix+"?name=%C3%89quipement", prefix+"?name=%C2%A0%C3%A9QUIPEMENT%E2%80%83"), 303)
	}
	t1 := client.Tag.Create().SetCompanyID(company).SetName("One").SaveX(ctx)
	t2 := client.Tag.Create().SetCompanyID(company).SetName("Two").SaveX(ctx)
	one(run(fmt.Sprintf("/tags/%d?name=Renamed", t1.ID), fmt.Sprintf("/tags/%d?name=+RENAMED+", t2.ID)), 303)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race parent").SaveX(ctx)
	codes := run(fmt.Sprintf("/tags/%d/delete", t1.ID), fmt.Sprintf("/customers/%d/tags/%d/attach", c.ID, t1.ID))
	if !((codes[0] == 303 && codes[1] == 404) || (codes[0] == 409 && codes[1] == 200)) {
		t.Fatalf("delete/attach race %v", codes)
	}
	var orphans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tag_links l LEFT JOIN tags t ON t.id=l.tag_id WHERE t.id IS NULL`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphans %d %v", orphans, err)
	}
}

func TestTagIndependentAssignedGrantsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "tag.attach", Scope: authorization.Assigned}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Assigned tagging", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Tagger").SetEmail("tagger@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Context").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned").SaveX(ctx)
	other := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Hidden").SaveX(ctx)
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(j.ID).SaveX(ctx)
	tag := client.Tag.Create().SetCompanyID(company).SetName("Selectable without config view").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, router, cookie, "GET", "/tags", 403)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/attach", j.ID, tag.ID), 200)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/detach", j.ID, tag.ID), 403)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/attach", other.ID, tag.ID), 404)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Detach only", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "tag.detach", Scope: authorization.Assigned}}, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/detach", j.ID, tag.ID), 200)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/tags/%d/attach", j.ID, tag.ID), 403)
}

func TestTagMixedViewAndActionScopesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	user := client.User.Create().SetCompanyID(company).SetName("Mixed scopes").SetEmail("mixed-tag@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Visible customer").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Visible project").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Mixed scope type").SaveX(ctx)
	asset := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Visible asset").SaveX(ctx)
	assigned := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned").SaveX(ctx)
	unassigned := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unassigned").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(assigned.ID).SetUserID(user.ID).SaveX(ctx)
	tag := client.Tag.Create().SetCompanyID(company).SetName("Mixed scope tag").SaveX(ctx)
	type parent struct {
		kind     string
		id       int64
		assigned bool
	}
	parents := []parent{{"customer", c.ID, false}, {"project", project.ID, false}, {"asset", asset.ID, false}, {"job", assigned.ID, true}, {"job", unassigned.ID, false}}
	for i, job := range []int64{assigned.ID, unassigned.ID, 0} {
		e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Scope estimate")
		inv := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(int64(2800 + i)).SetTitle("Scope invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now())
		if job != 0 {
			e.SetJobID(job)
			inv.SetJobID(job)
		}
		parents = append(parents, parent{"estimate", e.SaveX(ctx).ID, job == assigned.ID}, parent{"invoice", inv.SaveX(ctx).ID, job == assigned.ID})
	}
	for _, scenario := range []struct {
		name         string
		view, action authorization.Scope
	}{{"All View Assigned action", authorization.All, authorization.Assigned}, {"Assigned View All action", authorization.Assigned, authorization.All}, {"All View All action", authorization.All, authorization.All}, {"Assigned View Assigned action", authorization.Assigned, authorization.Assigned}} {
		t.Run(scenario.name, func(t *testing.T) {
			grants := []authorization.Grant{{Permission: "tag.attach", Scope: scenario.action}, {Permission: "tag.detach", Scope: scenario.action}}
			// A broad action may have a valid prerequisite on another parent;
			// that must not broaden this Job/document's independent View.
			if scenario.view == authorization.Assigned && scenario.action == authorization.All {
				grants = append(grants, authorization.Grant{Permission: "project.view", Scope: authorization.All})
			}
			for _, kind := range []string{"job", "estimate", "invoice"} {
				grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: scenario.view})
			}
			if scenario.view == authorization.All {
				for _, kind := range []string{"customer", "project", "asset"} {
					grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.All})
				}
			}
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, scenario.name, grants)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
				t.Fatal(err)
			}
			for _, p := range parents {
				want := 200
				if !p.assigned && !(p.kind == "project" && scenario.action == authorization.All) {
					if scenario.view == authorization.Assigned {
						want = 404
					} else if scenario.action == authorization.Assigned {
						want = 403
					}
				}
				path := fmt.Sprintf("/%ss/%d/tags/%d/", p.kind, p.id, tag.ID)
				expectStatus(t, router, cookie, "POST", path+"attach", want)
				expectStatus(t, router, cookie, "POST", path+"detach", want)
			}
		})
	}
}

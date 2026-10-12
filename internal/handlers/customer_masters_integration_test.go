package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/justinas/nosurf"
)

func TestCustomerMastersPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/customer-masters.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func masterPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values, want int) string {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w.Header().Get("Location")
}

func masterJSON(t *testing.T, router http.Handler, cookie *http.Cookie, path string, out any) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.AddCookie(cookie)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func TestCustomerChildArchiveSelectionAndPrimaryConcurrencyHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	for _, kind := range []string{"contact", "location"} {
		t.Run(kind, func(t *testing.T) {
			base := fmt.Sprintf("/customers/%d/%ss", c.ID, kind)
			values := url.Values{"first_name": {"Person"}}
			if kind == "location" {
				values = url.Values{"title": {"Site"}}
			}
			path := masterPost(t, router, cookie, base, values, 303)
			var child authorization.CustomerChild
			masterJSON(t, router, cookie, path, &child)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unfinished")
			if kind == "contact" {
				j.SetCustomerContactID(child.ID)
			} else {
				j.SetLocationID(child.ID)
			}
			job := j.SaveX(ctx)
			masterPost(t, router, cookie, path+"/delete", nil, 409)
			client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).SaveX(ctx)
			masterPost(t, router, cookie, path+"/delete", nil, 303)
			options := requestBody(t, router, cookie, "GET", base+"/options", 200)
			assertContains(t, options, "No "+kind)
			assertNotContains(t, options, child.Name())
			// A stale Job form must not select the archived record.
			jobValues := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Stale selection"}, "form_mode": {"create"}, "job_id": {"0"}}
			if kind == "contact" {
				jobValues.Set("customer_contact_id", fmt.Sprint(child.ID))
			} else {
				jobValues.Set("location_id", fmt.Sprint(child.ID))
			}
			r := httptest.NewRequest("POST", "/jobs", strings.NewReader(jobValues.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatalf("stale archived selection accepted: %d", w.Code)
			}
			masterPost(t, router, cookie, path+"/restore", nil, 303)
			var wg sync.WaitGroup
			codes := make(chan int, 8)
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					v := url.Values{"first_name": {"Concurrent"}, "is_primary": {"true"}}
					if kind == "location" {
						v = url.Values{"title": {"Concurrent"}, "is_primary": {"true"}}
					}
					r := httptest.NewRequest("POST", base, strings.NewReader(v.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					codes <- w.Code
				}()
			}
			wg.Wait()
			close(codes)
			for code := range codes {
				if code != 303 {
					t.Fatalf("primary change failed: %d", code)
				}
			}
			var rows []authorization.CustomerChild
			masterJSON(t, router, cookie, base, &rows)
			n := 0
			for _, row := range rows {
				if row.IsPrimary {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("%d primary records", n)
			}
		})
	}
}

func TestCustomerChildrenIndependentPermissionsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}, {Permission: "contact.create", Scope: authorization.All}, {Permission: "contact.edit", Scope: authorization.All}, {Permission: "contact.archive", Scope: authorization.All}, {Permission: "contact.archived.view", Scope: authorization.All}, {Permission: "contact.restore", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Contact clerk", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("clerk@test").SetName("Clerk").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Master").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	base := fmt.Sprintf("/customers/%d/contacts", c.ID)
	masterPost(t, router, cookie, base+"/not-an-id", url.Values{"first_name": {"Must not create"}}, 404)
	masterPost(t, router, cookie, base, url.Values{"first_name": {" "}}, 409)
	path := masterPost(t, router, cookie, base, url.Values{"first_name": {"Same"}, "email": {"same@test"}, "is_primary": {"true"}}, 303)
	masterPost(t, router, cookie, base, url.Values{"first_name": {"Same"}, "email": {"same@test"}}, 303)
	body := requestBody(t, router, cookie, "GET", base, 200)
	assertContains(t, body, "Primary")
	assertContains(t, body, "Same")
	masterPost(t, router, cookie, path, url.Values{"customer_id": {"999"}, "first_name": {"Moved"}}, 409)
	masterPost(t, router, cookie, path+"/delete", nil, 303)
	body = requestBody(t, router, cookie, "GET", base+"?archived=1", 200)
	assertContains(t, body, "Archived")
	masterPost(t, router, cookie, path+"/restore", nil, 303)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertNotContains(t, body, "Primary Contact</strong>")
	masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations", c.ID), url.Values{"title": {"Denied"}}, 404)
	masterPost(t, router, cookie, fmt.Sprintf("/customers/%d", c.ID), url.Values{"display_name": {"Denied"}}, 403)
}

func TestCustomerMasterIdentityHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	masterPost(t, router, cookie, "/customers", url.Values{"first_name": {"Cannot substitute"}}, 409)
	masterPost(t, router, cookie, "/customers", url.Values{"display_name": {"Named"}, "status": {"lead"}}, 409)
	for range 2 {
		path := masterPost(t, router, cookie, "/customers", url.Values{"display_name": {"Duplicate"}, "account_type": {"individual"}}, 303)
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertNotContains(t, body, `name="status"`)
		assertContains(t, body, "Organization")
		path = strings.TrimSuffix(path, "/edit")
		masterPost(t, router, cookie, path, url.Values{"account_type": {"company"}}, 303)
		body = requestBody(t, router, cookie, "GET", fmt.Sprintf("%s/edit", path), 200)
		assertContains(t, body, `value="company" selected`)
	}
}

func TestCustomerDashboardDoesNotPresentFailedMasterQueryAsEmpty(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Storage failure").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `ALTER TABLE customer_contacts RENAME TO temporarily_unavailable_contacts`); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 500)
}

func TestCustomerRelationshipSearchAndPagingHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Relation Owner").SaveX(ctx)
	path := masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/contacts", c.ID), url.Values{"first_name": {"Unique Person"}, "email": {"Needle@Example.test"}}, 303)
	body := requestBody(t, router, cookie, "GET", "/customers?search=nEeDlE", 200)
	assertContains(t, body, "Relation Owner")
	assertContains(t, body, "Contact match")
	assertContains(t, body, "Unique Person")
	masterPost(t, router, cookie, path+"/delete", nil, 303)
	body = requestBody(t, router, cookie, "GET", "/customers?search=needle", 200)
	assertNotContains(t, body, "Relation Owner")
	masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations", c.ID), url.Values{"title": {"Warehouse"}, "address_1": {"17 Rare Avenue"}}, 303)
	body = requestBody(t, router, cookie, "GET", "/customers?search=rArE", 200)
	assertContains(t, body, "Relation Owner")
	assertContains(t, body, "Location match")
	for i := 0; i < 52; i++ {
		client.Customer.Create().SetCompanyID(company).SetDisplayName(fmt.Sprintf("Page %02d", i)).SaveX(ctx)
	}
	body = requestBody(t, router, cookie, "GET", "/customers?search=Page&per_page=50", 200)
	assertContains(t, body, "Page 49")
	assertNotContains(t, body, "Page 50")
	assertContains(t, body, "per_page=50")
	if strings.Index(body, "Page 00") > strings.Index(body, "Page 49") {
		t.Fatal("unstable name order")
	}
	body = requestBody(t, router, cookie, "GET", "/customers?search=Page&per_page=50&page=2", 200)
	assertContains(t, body, "Page 50")
	assertNotContains(t, body, "Page 49")
}

func TestCustomerRelatedWorkFilteredBeforeCountsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Direct union").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Visible Project").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(project.ID).SetJobType("Visible Job").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("HIDDEN JOB").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Related reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "project.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("related@test").SetName("Reader").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(job.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 200)
	assertContains(t, body, "Visible Project")
	assertContains(t, body, "Visible Job")
	assertNotContains(t, body, "HIDDEN JOB")
	assertContains(t, body, "Projects (1)")
	assertContains(t, body, "Jobs (1)")
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/work?type=job", c.ID), 200)
	assertContains(t, body, "Visible Job")
	assertNotContains(t, body, "HIDDEN JOB")
	assertNotContains(t, body, "Visible Project")
}

func TestCustomerReceivablesAgingAndIndependentVisibilityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Aging").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Visible invoice Job").SaveX(ctx)
	var issued, draft, void int64
	for key, target := range map[string]*int64{"invoice:invoiced": &issued, "invoice:draft": &draft, "invoice:void": &void} {
		if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key=$1 LIMIT 1`, key).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Receivables reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("aging@test").SetName("Reader").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(job.ID).SaveX(ctx)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i, days := range []int{0, 1, 31, 61, 91, -1} {
		b := client.Invoice.Create().SetInvoiceDate(today).SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetTitle(fmt.Sprintf("Bucket %d", i)).SetStatusID(issued).SetLineItems(fmt.Sprintf(`[{"title":"Work","quantity":1,"unit_price":%d}]`, i+1))
		b.SetDueDate(time.Time{})
		if days >= 0 {
			b.SetDueDate(today.AddDate(0, 0, -days))
		}
		b.SetInvoiceNumber(int64(i + 1)).SaveX(ctx)
	}
	for i, status := range []int64{draft, void} {
		client.Invoice.Create().SetDueDate(today).SetInvoiceNumber(int64(7 + i)).SetInvoiceDate(today).SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetTitle("Excluded workflow").SetStatusID(status).SetLineItems(`[{"title":"Excluded","quantity":1,"unit_price":1000}]`).SaveX(ctx)
	}
	hidden := client.Invoice.Create().SetDueDate(today).SetInvoiceNumber(9).SetInvoiceDate(today).SetCompanyID(company).SetCustomerID(c.ID).SetTitle("HIDDEN RECEIVABLE").SetStatusID(issued).SetLineItems(`[{"title":"Hidden","quantity":1,"unit_price":9999}]`).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 200)
	assertContains(t, body, "$21.00")
	assertNotContains(t, body, "HIDDEN RECEIVABLE")
	assertNotContains(t, body, "$9999.00")
	for _, label := range []string{"Current", "1–30", "31–60", "61–90", "91+", "No Due Date"} {
		assertContains(t, body, label)
	}
	assertNotContains(t, body, "Customer Credit")
	_, err = settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "aging-credit"}, InvoiceID: hidden.ID, AmountCents: 1000000, Method: settlement.Cash, ReceivedDate: settlement.Date(today.Format("2006-01-02"))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Receivables and Credit reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "credit.view", Scope: authorization.All}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 200)
	assertContains(t, body, "$21.00")
	assertContains(t, body, "Available: $1.00")
	assertNotContains(t, body, "HIDDEN RECEIVABLE")
	assertNotContains(t, body, "Sources</h4>")
}

func TestCustomerArchivePreviewRestoreDoesNotReviveChildrenHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cascade").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	contact := masterPost(t, router, cookie, base+"/contacts", url.Values{"first_name": {"Former primary"}, "is_primary": {"true"}}, 303)
	location := masterPost(t, router, cookie, base+"/locations", url.Values{"title": {"Already archived"}, "is_primary": {"true"}}, 303)
	masterPost(t, router, cookie, location+"/delete", nil, 303)
	masterPost(t, router, cookie, base+"/delete", nil, 409)
	var preview struct{ Snapshot string }
	masterJSON(t, router, cookie, base+"/archive", &preview)
	if preview.Snapshot == "" {
		t.Fatal("missing explicit cascade review")
	}
	masterPost(t, router, cookie, base+"/delete", url.Values{"archive_snapshot": {preview.Snapshot}}, 303)
	expectStatus(t, router, cookie, "GET", base+"/edit", 403)
	masterPost(t, router, cookie, base+"/restore", nil, 303)
	for _, path := range []string{contact, location} {
		var row authorization.CustomerChild
		masterJSON(t, router, cookie, path, &row)
		if row.DeletedAt == nil || row.IsPrimary {
			t.Fatalf("child revived or primary retained: %+v", row)
		}
	}
	masterPost(t, router, cookie, contact+"/restore", nil, 303)
	var row authorization.CustomerChild
	masterJSON(t, router, cookie, contact, &row)
	if row.IsPrimary {
		t.Fatal("restore resurrected former primary")
	}
}

func TestCustomerChildAuditFailureAndCommitTimeRevocationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}, {Permission: "contact.create", Scope: authorization.All}, {Permission: "contact.edit", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, actor, 0, "Atomic editor", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("atomic@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	base := fmt.Sprintf("/customers/%d/contacts", c.ID)
	first := masterPost(t, router, cookie, base, url.Values{"first_name": {"Original primary"}, "is_primary": {"true"}}, 303)
	second := masterPost(t, router, cookie, base, url.Values{"first_name": {"Other"}}, 303)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_master_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.target_type='contact' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_master_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_master_audit()`); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, second, url.Values{"first_name": {"Must roll back"}, "is_primary": {"true"}}, 409)
	var original, other authorization.CustomerChild
	masterJSON(t, router, cookie, first, &original)
	masterJSON(t, router, cookie, second, &other)
	if !original.IsPrimary || other.IsPrimary || other.FirstName != "Other" {
		t.Fatal("audit failure partially changed primary or details")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_master_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='contact.edit'`, role); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", second, strings.NewReader(url.Values{"first_name": {"Revoked"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w.Code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mutation did not reach commit lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 403 {
		t.Fatalf("revoked mutation returned %d", code)
	}
	masterJSON(t, router, cookie, second, &other)
	if other.FirstName != "Other" {
		t.Fatal("revoked mutation committed")
	}
}

func TestCustomerChildSelectionArchiveRaceHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race").SaveX(ctx)
	for range 5 {
		path := masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations", c.ID), url.Values{"title": {"Race place"}}, 303)
		var place authorization.CustomerChild
		masterJSON(t, router, cookie, path, &place)
		start := make(chan struct{})
		codes := make(chan int, 2)
		post := func(path string, v url.Values) {
			<-start
			r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			codes <- w.Code
		}
		go post(path+"/delete", nil)
		go post("/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "job_type": {"Race job"}, "customer_id": {fmt.Sprint(c.ID)}, "location_id": {fmt.Sprint(place.ID)}})
		close(start)
		a, b := <-codes, <-codes
		if (a == 303) == (b == 303) {
			t.Fatalf("selection/archive must have exactly one winner: %d %d", a, b)
		}
	}
}

func TestCustomerArchiveRechecksWaitingJobSelectionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Parent archive race").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	var preview struct{ Snapshot string }
	masterJSON(t, router, cookie, base+"/archive", &preview)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var pid int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) locked`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 2)
	post := func(path string, v url.Values) {
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w.Code
	}
	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid))`, pid).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d waiting actions", count)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	go post(base+"/delete", url.Values{"archive_snapshot": {preview.Snapshot}})
	wait(1)
	go post("/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "job_type": {"Waiting Job"}, "customer_id": {fmt.Sprint(c.ID)}})
	wait(2)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := <-done, <-done
	if (a == 303) == (b == 303) {
		t.Fatalf("Customer archive and new selection both committed/rejected: %d %d", a, b)
	}
}

func TestCustomerChildAPIContextIsExplicitClosedAndReadOnly(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Context Customer").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	contactPath := masterPost(t, router, cookie, base+"/contacts", url.Values{"first_name": {"Explicit Person"}, "service_notes": {"Allowed Contact instruction"}, "notes": {"SECRET_INTERNAL"}}, 303)
	locationPath := masterPost(t, router, cookie, base+"/locations", url.Values{"title": {"Explicit place"}, "access_notes": {"Allowed access"}, "notes": {"SECRET_LOCATION"}}, 303)
	masterPost(t, router, cookie, base+"/contacts", url.Values{"first_name": {"SECRET_SIBLING"}, "is_primary": {"true"}}, 303)
	var contact, place authorization.CustomerChild
	masterJSON(t, router, cookie, contactPath, &contact)
	masterJSON(t, router, cookie, locationPath, &place)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetCustomerContactID(contact.ID).SetLocationID(place.ID).SetJobType("Explicit context").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("childcontext@test").SetName("Field").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(j.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Context device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := func(want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("API context status %d want %d: %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	body := get(200)
	assertContains(t, body, "Allowed Contact instruction")
	assertContains(t, body, "Allowed access")
	assertNotContains(t, body, "SECRET_")
	fieldCookie := sessionCookie(t, ctx, sessions, u.ID)
	webContext := requestBody(t, router, fieldCookie, "GET", fmt.Sprintf("/jobs/%d/customer-context", j.ID), 200)
	assertContains(t, webContext, "Allowed Contact instruction")
	assertNotContains(t, webContext, "SECRET_")
	expectStatus(t, router, fieldCookie, "GET", contactPath+"/edit", 404)
	masterPost(t, router, fieldCookie, contactPath, url.Values{"first_name": {"Forbidden"}}, 404)
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	get(404)
}

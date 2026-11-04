package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestAssignedContextLocationNavigation(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Context customer").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Service entrance").SetAddress1("123 Service Road").SetLatitude(41.25).SetLongitude(-87.75).SaveX(ctx)
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetLocationID(l.ID).SetJobType("Assigned work").SaveX(ctx)
	uid, _, cookie := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "location.view", Scope: authorization.Context}})
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	body := requestBody(t, f.router, cookie, "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), 200)
	assertContains(t, body, "41.25")
	assertContains(t, body, "-87.75")
	assertContains(t, body, "Navigate to service Location")
}

// Exercises the reference-client replacement/purge rule against actual authenticated
// HTTP. This is deliberately test-only; it is not a shipped offline client.
func TestAssignedContextClosedProjectionAndReferencePurge(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Visible Customer").SetFirstName("Visible Person").SetCompanyName("Visible Organization").SetPhone("555-0100").SetEmail("visible@example.test").SetServiceNotes("Customer service instruction").SetNotes("PRIVATE-INTERNAL").SetBillingCity("PRIVATE-BILLING").SetTaxExempt(true).SetCustomFields(`{"hidden":"PRIVATE-CUSTOM-FIELD"}`).SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Visible Location").SetAddress1("123 Service Road").SetAddress2("Unit 2").SetCity("Service City").SetState("IL").SetZipCode("12345").SetLatitude(41.25).SetLongitude(-87.75).SetNotes("PRIVATE-LOCATION").SaveX(ctx)
	contact := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName("Visible Contact").SetPhone("555-0101").SetEmail("contact@example.test").SaveX(ctx)
	if _, err := f.pool.Exec(ctx, `UPDATE locations SET access_notes='Access instruction',service_notes='Location service instruction' WHERE id=$1`, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE customer_contacts SET role_title='Site manager',service_notes='Contact instruction',notes='PRIVATE-CONTACT' WHERE id=$1`, contact.ID); err != nil {
		t.Fatal(err)
	}
	p := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetLocationID(l.ID).SetName("Visible Project").SetDescription("Operational description").SetServiceNotes("Project instruction").SetNotes("PRIVATE-PROJECT").SetCustomFields(`{"hidden":"PRIVATE-PROJECT-FIELD"}`).SaveX(ctx)
	a := f.asset(t, c.ID, "VISIBLE-ASSET")
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetCustomerContactID(contact.ID).SetLocationID(l.ID).SetProjectID(p.ID).SetAssetID(a.ID).SetJobType("Visible Job").SaveX(ctx)
	f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("PRIVATE-SIBLING-JOB").SaveX(ctx)
	primary := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName("PRIVATE-DEFAULT-CONTACT").SaveX(ctx)
	if _, err := f.pool.Exec(ctx, `UPDATE customer_contacts SET is_primary=true WHERE id=$1`, primary.ID); err != nil {
		t.Fatal(err)
	}
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}
	for _, kind := range []string{"customer", "contact", "location", "asset", "project"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.Context})
	}
	grants = append(grants, authorization.Grant{Permission: "comment.add", Scope: authorization.Assigned})
	uid, role, cookie := f.reader(t, grants)
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	token, err := f.sessions.CreateMobile(ctx, uid, "Closed context reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	get := func(path string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		assertNotContains(t, w.Body.String(), "PRIVATE-")
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	keys := func(m map[string]any, allowed string) {
		t.Helper()
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		sort.Strings(got)
		want := strings.Fields(allowed)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("projection keys %v want %v", got, want)
		}
	}
	base := fmt.Sprintf("/jobs/%d", j.ID)
	customer := get(base+"/customer-context", 200)
	keys(customer, "id display_name first_name last_name organization_name kind phone email service_notes")
	cache := get(base+"/master-context", 200)
	keys(cache, "contact contacts location project asset assets")
	keys(cache["contact"].(map[string]any), "id name role_title phone email service_notes")
	for _, v := range cache["contacts"].([]any) {
		keys(v.(map[string]any), "id name role_title phone email service_notes")
	}
	keys(cache["location"].(map[string]any), "id title address_1 address_2 city state zip_code latitude longitude access_notes service_notes")
	keys(cache["project"].(map[string]any), "name description status progress planned_start_date planned_end_date location service_notes")
	keys(cache["asset"].(map[string]any), "id number name ownership_class type status serial_number manufacturer model installed_at warranty_expires location service_notes lease_coverage")
	coverage := cache["asset"].(map[string]any)["lease_coverage"].(map[string]any)
	keys(coverage, "covered as_of")
	if coverage["covered"] != false {
		t.Fatalf("uncovered Asset: %v", coverage)
	}
	if _, err := time.Parse(time.RFC3339Nano, coverage["as_of"].(string)); err != nil {
		t.Fatal(err)
	}
	for _, v := range cache["assets"].([]any) {
		keys(v.(map[string]any), "id number name ownership_class type status serial_number manufacturer model installed_at warranty_expires location service_notes lease_coverage")
	}
	for _, suffix := range []string{"/customer-context", "/master-context"} {
		body := requestBody(t, f.router, cookie, "GET", base+suffix, 200)
		assertNotContains(t, body, "PRIVATE-")
		assertNotContains(t, body, "Lease")
	}
	assertNotContains(t, requestBody(t, f.router, cookie, "GET", "/search?q=PRIVATE", 200), "PRIVATE-SIBLING-JOB")
	expectStatus(t, f.router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 404)
	expectStatus(t, f.router, cookie, "GET", fmt.Sprintf("/projects/%d", p.ID), 403)
	// Replace, never merge, a successful snapshot: removed grants remove cached facets.
	if _, err = authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", grants[:2], 1); err != nil {
		t.Fatal(err)
	}
	cache = get(base+"/master-context", 200)
	if len(cache) != 0 {
		t.Fatalf("reference cache retained revoked facets: %v", cache)
	}
	if _, err = authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", grants, 2); err != nil {
		t.Fatal(err)
	}
	// A reference client may stage while its local lease is valid. Even that
	// previously authorized intent must pass fresh server authorization.
	leaseExpires := time.Now().Add(24 * time.Hour)
	pending := fmt.Sprintf(`{"operation_id":"23800000-0000-4000-8000-000000000001","content":"Pending field observation","occurred_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
	if _, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	cache = get(base+"/master-context", 404)
	customer = get(base+"/customer-context", 404)
	if len(cache) != 0 || len(customer) != 0 {
		t.Fatal("reference retained inaccessible synchronized context")
	}
	if !time.Now().Before(leaseExpires) {
		t.Fatal("fixture lease expired")
	}
	r := httptest.NewRequest("POST", base+"/comments", strings.NewReader(pending))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("lost assignment accepted staged work: %d %s", w.Code, w.Body.String())
	}
}

func TestAssignedContextPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Visible browser Customer").SetNotes("PRIVATE-BROWSER").SetServiceNotes("Browser operational instructions").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Browser service entrance").SetLatitude(41.25).SetLongitude(-87.75).SaveX(ctx)
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetLocationID(l.ID).SetJobType("Browser assigned Job").SaveX(ctx)
	uid, role, cookie := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "location.view", Scope: authorization.Context}})
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	server := httptest.NewServer(f.router)
	defer server.Close()
	output, err := exec.Command("node", "testdata/browser/assigned-context.cjs", server.URL, cookie.Value, f.cookie.Value, fmt.Sprint(role), fmt.Sprint(j.ID)).CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v %s", err, output)
	}
	t.Log(string(output))
	// The browser's Administrator action persisted the revocation.
	expectStatus(t, f.router, cookie, http.MethodGet, fmt.Sprintf("/jobs/%d/customer-context", j.ID), 404)
}

func TestAssignedCustomerContextRechecksWaitingAuthority(t *testing.T) {
	f := associationRepair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("WITHHELD-CUSTOMER").SaveX(ctx)
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Assigned work").SaveX(ctx)
	uid, role, _ := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}})
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	token, err := f.sessions.CreateMobile(ctx, uid, "Context reference")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	go func() {
		r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/customer-context", j.ID), nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		done <- w
	}()
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case w := <-done:
			t.Fatalf("context bypassed fence: %d %s", w.Code, w.Body.String())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1`, role); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 404 || strings.Contains(w.Body.String(), "WITHHELD-CUSTOMER") {
			t.Fatalf("stale context disclosed: %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// Queue a real grant change while the Customer read owns the authority fence.
// A combined HTML response must not mix that old Customer authorization with
// masters read after the queued revocation. No handler hooks or mocks are used.
func TestAssignedCustomerContextCombinedAuthority(t *testing.T) {
	f := associationRepair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("INTERLEAVED-CUSTOMER").SaveX(ctx)
	p := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetName("Context Project").SetDescription("BEFORE-REVOCATION").SaveX(ctx)
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("Assigned work").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "project.view", Scope: authorization.Context}}
	uid, role, cookie := f.reader(t, grants)
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	block, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	if _, err = block.Exec(ctx, `LOCK TABLE customers IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err = block.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/customer-context", j.ID), nil).WithContext(ctx)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		done <- w
	}()
	waitBehind := func(pid int) int {
		t.Helper()
		for {
			var waiting int
			if err := f.pool.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, pid).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting != 0 {
				return waiting
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	reader := waitBehind(blocker)
	revoked := make(chan error, 1)
	go func() {
		// Match the production authority fence and atomically mark the new
		// generation so the HTTP body itself reveals a mixed snapshot.
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			revoked <- err
			return
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, role)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE projects SET description='AFTER-REVOCATION' WHERE id=$1`, p.ID)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		revoked <- err
	}()
	waitBehind(reader)
	if err = block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	w := <-done
	// A coherent snapshot completed before revocation may be returned. If the
	// request crossed into a second transaction after revocation, it must deny.
	// Queue a second request to prove current authority is now gone as well.
	if w.Code != 200 && w.Code != 404 {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
	if w.Code == 200 && strings.Contains(w.Body.String(), "AFTER-REVOCATION") {
		t.Fatalf("mixed authority generations disclosed Customer after revocation: %s", w.Body.String())
	}
	expectStatus(t, f.router, cookie, "GET", fmt.Sprintf("/jobs/%d/customer-context", j.ID), 404)
}

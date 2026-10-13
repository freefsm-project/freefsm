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
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestJobPricingCapturedThroughProductionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Pricing customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Catalog work").SetDescription("Catalog description").SetUnitPrice(47.25).SetTaxable(true).SetTaxRate("8.25").SetUnitCost(987.61).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Captured work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	lines := fmt.Sprintf(`[{"item_id":%d,"title":"Captured work","description":"Captured description","unit_price":47.25,"quantity":2,"taxable":true,"tax_rate":"8.25"},{"title":"Free text","unit_price":12.50,"quantity":1}]`, i.ID)
	itemPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {lines}}, 303)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "47.25")
	assertContains(t, body, "Free text")
	assertNotContains(t, body, "987.61")
	form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	assertContains(t, form, "data-line-item-editor")
	assertContains(t, form, "Captured description")
	assertNotContains(t, form, "987.61")
}

func TestJobPricingAuditFailureAndValidationRollbackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic pricing customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unchanged job").SetLineItems(`[{"title":"Recorded free text","unit_price":5,"quantity":1}]`).SaveX(ctx)
	j = client.Job.GetX(ctx, j.ID) // Freeze PostgreSQL's recorded JSON representation.
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	for _, raw := range []string{`[{"title":"Cost injection","quantity":1,"unit_price":5,"unit_cost":987.61}]`, `[{"title":"Invalid quantity","quantity":0,"unit_price":5}]`, `[{"title":"Invalid tax","quantity":1,"unit_price":5,"tax_rate":"101"}]`, `[{"title":"Invalid provenance","quantity":1,"unit_price":5,"item_id":-1}]`} {
		itemPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Rejected title"}, "line_items": {raw}}, 422)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_job_pricing_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='job' AND NEW.action='pricing.view' THEN RAISE EXCEPTION 'injected pricing audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_job_pricing_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_job_pricing_audit()`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Rejected title"}, "line_items": {`[{"title":"Must roll back","quantity":1,"unit_price":7}]`}}, 500)
	if got := client.Job.GetX(ctx, j.ID); got.JobType != j.JobType || got.LineItems != j.LineItems {
		t.Fatalf("partial Job effect: %+v", got)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_job_pricing_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {`[{"title":"Accepted pricing","quantity":1,"unit_price":7}]`}}, 303)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND action='pricing.view' AND actor_id=$2 AND scope='A' AND channel='web'`, j.ID, admin).Scan(&n); err != nil || n != 1 {
		t.Fatalf("atomic pricing audit count=%d: %v", n, err)
	}
}

func TestJobPricingWaitingSelectionHTTP(t *testing.T) {
	for _, change := range []string{"archive", "pricebook-revoke", "pricing-revoke", "price-drift"} {
		t.Run(change, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Waiting capture").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Waiting job").SaveX(ctx)
			i := client.Item.Create().SetCompanyID(company).SetName("Waiting Item").SetUnitPrice(50).SaveX(ctx)
			u := client.User.Create().SetCompanyID(company).SetName("Pricing writer").SetEmail("waiting44@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			svc := authorization.New(pool)
			grants := []authorization.Grant{}
			for _, p := range []string{"job.view", "job.edit", "pricing.view", "item.view", "pricebook.view"} {
				grants = append(grants, authorization.Grant{Permission: p, Scope: authorization.All})
			}
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting pricing writer", grants)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/jobs/%d", j.ID)
			values := currentJobFormValues(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Accepted after drift"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Captured before drift","unit_price":31.25,"quantity":1,"tax_rate":"7.25"}]`, i.ID)}})
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=190019 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("pricing write did not reach authority fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch change {
			case "archive":
				_, err = blocker.Exec(ctx, `UPDATE items SET deleted_at=now() WHERE id=$1`, i.ID)
			case "pricebook-revoke":
				_, err = blocker.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='pricebook.view'`, role)
			case "pricing-revoke":
				_, err = blocker.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='pricing.view'`, role)
			case "price-drift":
				_, err = blocker.Exec(ctx, `UPDATE items SET unit_price=99,tax_rate='9.5',description='Changed catalog description' WHERE id=$1`, i.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var w *httptest.ResponseRecorder
			select {
			case w = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("pricing write remained blocked")
			}
			want := 403
			if change == "archive" {
				want = 409
			}
			if change == "price-drift" {
				want = 303
			}
			if w.Code != want {
				t.Fatalf("status=%d want=%d: %s", w.Code, want, w.Body.String())
			}
			got := client.Job.GetX(ctx, j.ID)
			if change == "price-drift" {
				lines, _ := services.DecodeLineItems(got.LineItems)
				if len(lines) != 1 || lines[0].UnitPrice != 31.25 || lines[0].TaxRate != "7.25" {
					t.Fatalf("silently repriced: %s", got.LineItems)
				}
			} else if got.LineItems != j.LineItems || got.JobType != j.JobType {
				t.Fatal("rejected write had effects")
			}
		})
	}
}

func TestJobPricingPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser pricing customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser pricing job").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Browser capture Item").SetDescription("Captured browser description").SetUnitPrice(47.25).SetUnitCost(987.61).SetTaxable(true).SetTaxRate("8.25%").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	run := func(mode string) {
		t.Helper()
		bctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		out, err := exec.CommandContext(bctx, "node", "testdata/browser/job-pricing.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID), fmt.Sprint(i.ID), mode).CombinedOutput()
		t.Logf("browser %s: %s", mode, out)
		if err != nil {
			t.Fatal(err)
		}
	}
	run("capture")
	before := client.Job.GetX(ctx, j.ID).LineItems
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"name": {"Catalog renamed"}, "description": {"Catalog changed"}, "unit_price": {"99"}, "tax_rate": {"0"}}, 303)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", i.ID), url.Values{}, 303)
	if client.Job.GetX(ctx, j.ID).LineItems != before {
		t.Fatal("catalog edit rewrote browser capture")
	}
	run("edit")
	lines, err := services.DecodeLineItems(client.Job.GetX(ctx, j.ID).LineItems)
	if err != nil || len(lines) != 2 || lines[0].ItemID != i.ID || lines[0].UnitPrice != 22.75 || lines[0].TaxRate != "8.25%" || lines[1].ItemID != 0 {
		t.Fatalf("browser pricing persistence: %+v %v", lines, err)
	}
}

func TestJobPricingReferenceHarnessThroughProductionAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Reference catalog").SetUnitPrice(47.25).SetUnitCost(987.61).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference job").SetLineItems(fmt.Sprintf(`[{"item_id":%d,"title":"Reference captured work","unit_price":31.25,"quantity":1}]`, i.ID)).SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Reference user").SetRole("tech").SetEmail("reference44@test").SetPasswordHash("hash").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Reference field pricing", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Job pricing reference")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	out, err := exec.Command("node", "../api/v1/testdata/job-pricing-reference-conformance.cjs", server.URL, token, fmt.Sprint(j.ID), fmt.Sprint(i.ID)).CombinedOutput()
	t.Logf("reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobPricingSessionExpiryBeforeCommitHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Expiring Job capture").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unchanged on expiry").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Blocked selection").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	values := currentJobFormValues(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Expired capture","unit_price":12,"quantity":1}]`, i.ID)}})
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM items WHERE id=$1 FOR UPDATE`, i.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='transactionid' AND query LIKE '%FROM items%FOR SHARE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selection did not reach Item lock before expiry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(2100 * time.Millisecond)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 {
			t.Fatalf("expired commit: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("expired request remained blocked")
	}
	if got := client.Job.GetX(ctx, j.ID); got.LineItems != "[]" {
		t.Fatalf("expired action committed %s", got.LineItems)
	}
}

func TestJobPricingSnapshotProvenanceHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Snapshot customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Original catalog").SetUnitPrice(50).SetUnitCost(987.61).SaveX(ctx)
	other := client.Item.Create().SetCompanyID(company).SetName("Different provenance").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Snapshot job").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	post := func(raw string, status int) {
		t.Helper()
		itemPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {raw}}, status)
	}
	post(fmt.Sprintf(`[{"item_id":%d,"title":"Captured title","description":"Original description","unit_price":31.25,"quantity":2,"taxable":true,"tax_rate":"8.25"},{"title":"Historical free text","unit_price":4,"quantity":1}]`, i.ID), 303)
	stored := client.Job.GetX(ctx, j.ID).LineItems
	var lines []map[string]any
	if err := json.Unmarshal([]byte(stored), &lines); err != nil {
		t.Fatal(err)
	}
	if lines[0]["snapshot_id"] == nil || lines[1]["snapshot_id"] == nil {
		t.Fatal("captured lines have no stable snapshot identity")
	}
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"name": {"Catalog drift"}, "description": {"Changed description"}, "unit_price": {"99"}, "taxable": {"false"}, "tax_rate": {"0"}}, 303)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", i.ID), url.Values{}, 303)
	if got := client.Job.GetX(ctx, j.ID).LineItems; got != stored {
		t.Fatal("catalog changed captured Job JSON")
	}
	lines[0]["title"] = "Edited snapshot"
	lines[0]["unit_price"] = 22.75
	raw, _ := json.Marshal(lines)
	post(string(raw), 303)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Edited snapshot")
	assertContains(t, body, "22.75")
	lines[0]["item_id"] = other.ID
	raw, _ = json.Marshal(lines)
	post(string(raw), 409)
	lines[0]["item_id"] = i.ID
	delete(lines[0], "snapshot_id") // Replacement is a new selection, even if counts match.
	raw, _ = json.Marshal(lines)
	post(string(raw), 409)
	got, _ := services.DecodeLineItems(client.Job.GetX(ctx, j.ID).LineItems)
	if got[0].ItemID != i.ID || got[1].ItemID != 0 {
		t.Fatal("provenance rewritten")
	}
}

func TestJobPricingIndependentGrantsHTTPAndAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Pricing context").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Scoped work").SetLineItems(`[{"title":"Private captured work","unit_price":47.25,"quantity":1}]`).SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Pricing reader").SetEmail("pricing44@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.edit", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.Context}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Pricing-independent planner", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	for _, suffix := range []string{"", "/edit"} {
		body := requestBody(t, router, cookie, "GET", path+suffix, 200)
		assertNotContains(t, body, "Private captured work")
		assertNotContains(t, body, "47.25")
	}
	itemPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {`[{"title":"Forbidden replacement","unit_price":5,"quantity":1}]`}}, 403)
	grants = append(grants, authorization.Grant{Permission: "pricing.view", Scope: authorization.Assigned})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Captured pricing without catalog", grants, 1); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Pricing reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := func(path string, status int) string {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := get(path, 200)
	assertContains(t, body, "Private captured work")
	assertContains(t, body, `"unit_price":47.25`)
	body = get(path+"/customer-context", 200)
	assertContains(t, body, "Pricing context")
	for _, secret := range []string{"line_items", "unit_price", "Private captured work", "items", "pricing"} {
		assertNotContains(t, body, secret)
	}
	get("/items", 404)
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	body = get(path, 200) // All Job View remains; Assigned pricing does not.
	assertNotContains(t, body, "line_items")
	assertNotContains(t, body, "billing_type")
	body = get("/jobs", 200)
	assertContains(t, body, "Scoped work")
	assertNotContains(t, body, "Private captured work")
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No captured pricing", grants[:len(grants)-1], 2); err != nil {
		t.Fatal(err)
	}
	body = get(path, 200)
	assertNotContains(t, body, "line_items")
	assertNotContains(t, body, "billing_type")
}

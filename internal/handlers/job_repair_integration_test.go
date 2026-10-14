package handlers

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
)

func jobRepairRevision(t *testing.T, router http.Handler, cookie *http.Cookie, path string) string {
	t.Helper()
	body := requestBody(t, router, cookie, "GET", path, 200)
	match := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing server form revision")
	}
	return match[1]
}

// Preserves the merger's office-reader setup: actual HTTP membership, no
// singleton, no assignment, no planning/relationship Edit, and no sibling leak.
func TestJobRepairOfficeContactsHTTP(t *testing.T) { jobRepairOfficeContacts(t, false) }
func TestJobRepairOfficeContactsBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	jobRepairOfficeContacts(t, true)
}
func jobRepairOfficeContacts(t *testing.T, browser bool) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Office reader owner").SaveX(ctx)
	a := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("REVIEW_LINKED_ALPHA").SaveX(ctx)
	b := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("REVIEW_LINKED_BETA").SaveX(ctx)
	client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("REVIEW_UNLINKED_SIBLING").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Read-only office context").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	revision := jobRepairRevision(t, router, adminCookie, path+"/contacts")
	w := jobRepairRawPost(router, adminCookie, path+"/contacts", url.Values{"contact_ids": {fmt.Sprint(a.ID), fmt.Sprint(b.ID)}, "save_action_id": {newOperationKey()}, "save_revision": {revision}})
	if w.Code != 303 {
		t.Fatalf("persist explicit Contacts: %d %s", w.Code, w.Body.String())
	}
	var members []int64
	if err := pool.QueryRow(ctx, `SELECT array_agg(contact_id ORDER BY contact_id) FROM job_contacts WHERE job_id=$1`, j.ID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0] != a.ID || members[1] != b.ID || client.Job.GetX(ctx, j.ID).CustomerContactID != nil {
		t.Fatal("explicit membership fixture must have two Contacts and no singleton")
	}
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Office reader only", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Office reader").SetEmail("job-repair-office@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, a.FirstName)
	assertContains(t, body, b.FirstName)
	assertNotContains(t, body, "REVIEW_UNLINKED_SIBLING")
	assertNotContains(t, body, "Manage relationships")
	expectStatus(t, router, cookie, "GET", path+"/contacts", 403)
	expectStatus(t, router, cookie, "GET", path+"/master-context", 404)
	if browser {
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
		server := httptest.NewServer(mux)
		defer server.Close()
		ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "node", "testdata/browser/job-office-contacts.cjs", server.URL, cookie.Value, path).CombinedOutput()
		t.Logf("browser: %s", out)
		if err != nil {
			t.Fatal(err)
		}
	}
	limited, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Job reader without Contact View", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, limited); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertNotContains(t, body, a.FirstName)
	assertNotContains(t, body, b.FirstName)
}

// Deliberately sends EXACT caller metadata. Never fetches a fresh form or
// repairs missing transport fields: these are the independent merger's probes.
func jobRepairRawPost(router http.Handler, cookie *http.Cookie, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

// Existing positive form tests submit the current authorized form, including
// its mandatory hidden guard. Explicit metadata is never replaced. Adversarial
// omission/stale tests use jobRepairRawPost (or a direct request), not this helper.
func currentJobFormValues(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values) url.Values {
	t.Helper()
	if strings.HasSuffix(path, "/status") && strings.HasPrefix(path, "/jobs/") && !values.Has("operation_id") {
		body := requestBody(t, router, cookie, "GET", strings.TrimSuffix(path, "/status"), 200)
		out := url.Values{}
		for k, v := range values {
			out[k] = append([]string(nil), v...)
		}
		for _, key := range []string{"operation_id", "status_revision", "meaning_" + values.Get("status_id")} {
			match := regexp.MustCompile(`name="` + key + `" value="([^"]+)"`).FindStringSubmatch(body)
			if len(match) == 2 {
				out.Set(key, match[1])
			}
		}
		return out
	}
	values = currentCommercialFormValues(t, router, cookie, path, values)
	if !regexp.MustCompile(`^/jobs/[1-9][0-9]*(/(contacts|links|assets))?$`).MatchString(path) || values.Has("save_action_id") || values.Has("save_revision") {
		return values
	}
	form := path
	if strings.Count(path, "/") == 2 {
		form += "/edit"
	}
	r := httptest.NewRequest("GET", form, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		return values
	} // Preserve the ordinary authority/error test.
	match := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("current Job form omitted required revision")
	}
	out := url.Values{}
	for k, v := range values {
		out[k] = append([]string(nil), v...)
	}
	out.Set("save_revision", match[1])
	out.Set("save_action_id", newOperationKey())
	if match := regexp.MustCompile(`name="field_baseline" value="([^"]*)"`).FindStringSubmatch(w.Body.String()); len(match) == 2 {
		out.Set("field_baseline", html.UnescapeString(match[1]))
	}
	return out
}

// Supporting domain-transaction tests use the real admission and revision
// interfaces when deliberately calling an authenticated service without HTTP.
func admitCurrentJobRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64) context.Context {
	t.Helper()
	req, ok := authorization.FromContext(ctx)
	if !ok {
		t.Fatal("missing domain actor")
	}
	path := fmt.Sprintf("/jobs/%d", id)
	revision, err := saveaction.Revision(ctx, pool, req.Actor.UserID, path)
	if err != nil {
		t.Fatal(err)
	}
	a := saveaction.Action{UserID: req.Actor.UserID, ID: newOperationKey(), Revision: revision, Path: path, Atomic: true, Outcome: &saveaction.Outcome{}}
	admitted, err := (saveaction.Store{Pool: pool}).Admit(ctx, a, "supporting-domain-test", "POST", path, path+"/edit")
	if err != nil || !admitted {
		t.Fatalf("admit domain update: %v", err)
	}
	return saveaction.WithAction(ctx, a)
}

// Derived from merge-33/source/internal/handlers/merge33review_job_contract_test.go.
// Retains all four original omission probes and extends the same public seam to
// planning/replacement, revision-only requests, malformed and explicit stale guards.
func TestJobRepairRequiredRevisionHTTP(t *testing.T) {
	for _, kind := range []string{"contacts", "links", "planning", "replacement"} {
		for _, metadata := range []string{"none", "action_only", "revision_only", "malformed", "stale"} {
			t.Run(kind+"/"+metadata, func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original owner").SaveX(ctx)
				n := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement owner").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Original title").SaveX(ctx)
				first, second := int64(0), int64(0)
				path := fmt.Sprintf("/jobs/%d", j.ID)
				form := path + "/edit"
				if kind == "contacts" {
					first = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Accepted Contact").SaveX(ctx).ID
					second = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Stale Contact").SaveX(ctx).ID
					path += "/contacts"
					form = path
				} else if kind == "links" {
					first = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Accepted Location").SaveX(ctx).ID
					second = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Stale Location").SaveX(ctx).ID
					path += "/links"
					form = path
				}
				input := func(stale bool) url.Values {
					id := first
					if stale {
						id = second
					}
					if kind == "contacts" {
						return url.Values{"contact_ids": {fmt.Sprint(id)}}
					}
					if kind == "links" {
						return url.Values{"project_id": {"0"}, "asset_id": {"0"}, "location_id": {fmt.Sprint(id)}}
					}
					v := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Accepted title"}}
					if stale {
						v.Set("job_type", "Stale title")
						if kind == "replacement" {
							v.Set("customer_id", fmt.Sprint(n.ID))
							v.Set("replace_customer", "true")
						}
					}
					return v
				}
				old := jobRepairRevision(t, router, cookie, form)
				if other := jobRepairRevision(t, router, cookie, form); old != other {
					t.Fatal("initial editors did not share a baseline")
				}
				accepted := input(false)
				accepted.Set("save_action_id", newOperationKey())
				accepted.Set("save_revision", old)
				if w := jobRepairRawPost(router, cookie, path, accepted); w.Code != 303 {
					t.Fatalf("guarded initial save: %d %s", w.Code, w.Body.String())
				}
				const snapshot = `SELECT jsonb_build_object('job',to_jsonb(j),'contacts',(SELECT coalesce(jsonb_agg(contact_id ORDER BY contact_id),'[]') FROM job_contacts WHERE job_id=j.id))::text FROM jobs j WHERE id=$1`
				var before, after string
				var auditsBefore, auditsAfter int
				if err := pool.QueryRow(ctx, snapshot, j.ID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1`, j.ID).Scan(&auditsBefore); err != nil {
					t.Fatal(err)
				}
				current := jobRepairRevision(t, router, cookie, form)
				if old == current {
					t.Fatal("accepted mutation did not invalidate old revision")
				}
				stale := input(true)
				switch metadata {
				case "action_only":
					stale.Set("save_action_id", newOperationKey())
				case "revision_only":
					stale.Set("save_revision", old)
				case "malformed":
					stale.Set("save_action_id", newOperationKey())
					stale.Set("save_revision", "not-a-revision")
				case "stale":
					stale.Set("save_action_id", newOperationKey())
					stale.Set("save_revision", old)
				}
				w := jobRepairRawPost(router, cookie, path, stale)
				if w.Code != 400 && w.Code != 409 && w.Code != 428 {
					t.Errorf("unguarded/stale mutation accepted: %d %s", w.Code, w.Body.String())
				}
				if err := pool.QueryRow(ctx, snapshot, j.ID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1`, j.ID).Scan(&auditsAfter); err != nil {
					t.Fatal(err)
				}
				if before != after || auditsBefore != auditsAfter {
					t.Errorf("rejected intent changed state or success audit (%d -> %d)", auditsBefore, auditsAfter)
				}
			})
		}
	}
}

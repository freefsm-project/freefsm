package handlers

import (
	"context"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/services"
	"golang.org/x/net/html"
)

func compositionField(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatalf("missing %s", name)
	}
	return stdhtml.UnescapeString(m[1])
}

func compositionComparison(t *testing.T, body string) (map[string]string, *html.Node) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	walkSaveNodes(doc, func(summary *html.Node) {
		if !hasSaveAttr(summary, "data-error-summary") {
			return
		}
		walkSaveNodes(summary, func(n *html.Node) {
			if n.Data != "dt" {
				return
			}
			for next := n.NextSibling; next != nil; next = next.NextSibling {
				if next.Type == html.ElementNode && next.Data == "dd" {
					values[saveNodeText(n)] = saveNodeText(next)
					break
				}
			}
		})
	})
	return values, doc
}

func TestGuardedConflictComparisonTracksControlStateHTTP(t *testing.T) {
	t.Run("unchecked current boolean and checked proposal", func(t *testing.T) {
		_, client, router, sessions, company, admin := permissionHTTPFixture(t)
		ctx := context.Background()
		cookie := sessionCookie(t, ctx, sessions, admin)
		item := client.Item.Create().SetCompanyID(company).SetName("Original Item").SetTaxable(false).SaveX(ctx)
		path := fmt.Sprintf("/items/%d", item.ID)
		form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
		itemPost(t, router, cookie, path, url.Values{"name": {"Current Item"}}, 303)
		proposal := url.Values{"save_action_id": {compositionField(t, form, "save_action_id")}, "save_history": {compositionField(t, form, "save_history")}, "save_revision": {compositionField(t, form, "save_revision")}, "name": {"Proposed Item"}, "taxable": {"true"}}
		conflict := itemPost(t, router, cookie, path, proposal, 409)
		current, doc := compositionComparison(t, conflict)
		if current["taxable"] != "Not checked (value: true)" {
			t.Fatalf("false current checkbox described as %q", current["taxable"])
		}
		checked := false
		walkSaveNodes(doc, func(n *html.Node) {
			if n.Data == "input" && saveAttr(n, "name") == "taxable" {
				checked = hasSaveAttr(n, "checked")
			}
		})
		if !checked {
			t.Fatal("true checkbox proposal lost")
		}
		proposal.Set("save_action_id", newOperationKey())
		proposal.Set("save_conflict_review", compositionField(t, conflict, "save_conflict_review"))
		itemPost(t, router, cookie, path, proposal, 303)
		assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Proposed Item")
	})
	t.Run("all current multiselect choices remain distinct from proposal", func(t *testing.T) {
		_, client, router, sessions, company, admin := permissionHTTPFixture(t)
		ctx := context.Background()
		cookie := sessionCookie(t, ctx, sessions, admin)
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"estimate"}, "name": {"Equipment"}, "field_type": {"multiselect"}, "options": {`["Pump","Motor","Valve"]`}}, 303)
		customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Comparison parent").SaveX(ctx)
		values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Original estimate"}, "tax_rate": {"0"}, "line_items": {"[]"}, "custom_field_1[]": {"Pump", "Motor"}}
		itemPost(t, router, cookie, "/estimates", values, 303)
		id := client.Estimate.Query().OnlyX(ctx).ID
		path := fmt.Sprintf("/estimates/%d", id)
		form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
		values.Set("title", "Current estimate")
		itemPost(t, router, cookie, path, values, 303)
		values.Set("save_action_id", compositionField(t, form, "save_action_id"))
		values.Set("save_history", compositionField(t, form, "save_history"))
		values.Set("save_revision", compositionField(t, form, "save_revision"))
		values.Set("title", "Proposed estimate")
		values["custom_field_1[]"] = []string{"Valve"}
		conflict := itemPost(t, router, cookie, path, values, 409)
		current, doc := compositionComparison(t, conflict)
		if current["custom_field_1[]"] != "Pump, Motor" {
			t.Fatalf("lost current choices: %q", current["custom_field_1[]"])
		}
		selected := []string{}
		walkSaveNodes(doc, func(n *html.Node) {
			if n.Data == "select" && saveAttr(n, "name") == "custom_field_1[]" {
				walkSaveNodes(n, func(o *html.Node) {
					if o.Data == "option" && hasSaveAttr(o, "selected") {
						selected = append(selected, saveAttr(o, "value"))
					}
				})
			}
		})
		if len(selected) != 1 || selected[0] != "Valve" {
			t.Fatalf("proposal replaced by current choices: %v", selected)
		}
		values.Set("save_action_id", newOperationKey())
		values.Set("save_conflict_review", compositionField(t, conflict, "save_conflict_review"))
		itemPost(t, router, cookie, path, values, 303)
		assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Proposed estimate")
	})
}

func TestBackupControlsUsePersistedPresentationAndCurrentNavigationHTTP(t *testing.T) {
	pool, client, main, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	itemPost(t, main, cookie, "/preferences", url.Values{"theme": {"dark"}, "font_size": {"large"}}, 303)
	backupCookie := sessionCookie(t, ctx, sessions, admin)
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := backup.New(backup.Config{DSN: pool.Config().ConnString(), UploadDir: t.TempDir(), StateDir: control.StateDir(), Control: control, BuildKind: backup.BuildDevelopment, ResetConnections: pool.Reset})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown()
	router := NewBackupRouter(pool, manager, control, services.NewUserService(client), services.NewCompanySettingsService(client), sessions, "", backupTestActivityHandler(client))
	get := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(backupCookie)
		r.AddCookie(&http.Cookie{Name: "theme", Value: "light"})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("backup GET %s=%d want%d: %s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	body := get("/settings/backup", 200).Body.String()
	assertContains(t, body, `data-theme="dark"`)
	assertContains(t, body, `data-font-size="large"`)
	assertContains(t, body, `href="/items"`)
	assertContains(t, body, `href="/directory"`)
	assertContains(t, body, `href="/settings/statuses/job"`)
	assertContains(t, body, `href="/settings/backup" class="active" aria-current="page"`)
	// Legacy role provenance is immutable. Explicitly assign current authority
	// to a distinct legacy-tech account rather than rewriting recorded history.
	legacyTech := client.User.Create().SetCompanyID(company).SetName("Explicit protected Role holder").SetEmail("backup-protected-tech@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, legacyTech.ID, 1); err != nil {
		t.Fatal(err)
	}
	techBody := requestBody(t, router, sessionCookie(t, ctx, sessions, legacyTech.ID), "GET", "/settings/backup", 200)
	assertContains(t, techBody, `href="/items"`)
	assertContains(t, techBody, `href="/directory"`)
	assertContains(t, techBody, `href="/settings/statuses/job"`)
	itemPost(t, main, cookie, "/preferences", url.Values{"theme": {"light"}, "font_size": {"small"}}, 303)
	body = get("/settings/backup", 200).Body.String()
	assertContains(t, body, `data-theme="light"`)
	assertContains(t, body, `data-font-size="small"`)
	if _, err := pool.Exec(ctx, `ALTER TABLE user_presentation RENAME TO temporarily_unavailable_presentation`); err != nil {
		t.Fatal(err)
	}
	pool.Reset()
	unavailable := get("/settings/backup", 503)
	for _, c := range unavailable.Result().Cookies() {
		if c.Name == "session" && c.MaxAge < 0 {
			t.Fatal("presentation outage discarded valid session")
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE temporarily_unavailable_presentation RENAME TO user_presentation`); err != nil {
		t.Fatal(err)
	}
	get("/settings/backup", 200)
	pool.Close()
	get("/settings/backup", 503)
	shell := get("/settings/backup/status-page", 200).Body.String()
	assertContains(t, shell, `data-status-only="true"`)
	assertNotContains(t, shell, `id="sidebar"`)
	assertNotContains(t, shell, `href="/items"`)
}

func TestGuardedTimeEntryTwoEditorsPreserveProtectedContextHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Time correction only", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.correct", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	actor := client.User.Create().SetCompanyID(company).SetName("Time editor").SetEmail("guarded-time@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, actor.ID, role); err != nil {
		t.Fatal(err)
	}
	subject := client.User.Create().SetCompanyID(company).SetName("Recorded subject").SetEmail("recorded-time@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private time parent").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("PRIVATE LINKED JOB").SaveX(ctx)
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(subject.ID).SetJobID(job.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetLatitude(51.234567).SetLongitude(12.345678).SetNotes("Original notes").SaveX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	firstCookie, secondCookie := sessionCookie(t, ctx, sessions, actor.ID), sessionCookie(t, ctx, sessions, actor.ID)
	first := requestBody(t, router, firstCookie, "GET", path+"/edit", 200)
	second := requestBody(t, router, secondCookie, "GET", path+"/edit", 200)
	for _, body := range []string{first, second} {
		assertNotContains(t, body, `name="job_id"`)
		assertNotContains(t, body, "PRIVATE LINKED JOB")
		assertNotContains(t, body, "51.234567")
	}
	input := func(form, notes string) url.Values {
		return url.Values{"reason": {"Reviewed time correction"}, "save_action_id": {compositionField(t, form, "save_action_id")}, "save_history": {compositionField(t, form, "save_history")}, "save_revision": {compositionField(t, form, "save_revision")}, "notes": {notes}, "user_id": {fmt.Sprint(actor.ID)}}
	}
	// GPS is clock-only immutable data. Reject the entire attempted correction
	// rather than silently dropping supplied coordinates and reporting success.
	invalidGPS := input(first, "Must not save")
	invalidGPS.Set("latitude", "0")
	invalidGPS.Set("longitude", "0")
	itemPost(t, router, firstCookie, path, invalidGPS, 400)
	assertContains(t, requestBody(t, router, firstCookie, "GET", path, 200), "Original notes")
	first = requestBody(t, router, firstCookie, "GET", path+"/edit", 200)
	one, two := input(first, "First correction"), input(second, "Second proposed correction")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_time_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.completed_at IS NOT NULL THEN RAISE EXCEPTION 'lost completion'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_time_completion BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION fail_time_completion()`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, firstCookie, path, one, 303)
	assertContains(t, requestBody(t, router, firstCookie, "GET", "/save-actions/"+one.Get("save_action_id"), 200), `"disposition":"saved"`)
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_time_completion ON online_save_actions`); err != nil {
		t.Fatal(err)
	}
	conflict := itemPost(t, router, secondCookie, path, two, 409)
	assertContains(t, conflict, "First correction")
	assertContains(t, conflict, "Second proposed correction")
	assertNotContains(t, conflict, "PRIVATE LINKED JOB")
	assertNotContains(t, conflict, "51.234567")
	assertNotContains(t, conflict, `name="job_id"`)
	assertContains(t, requestBody(t, router, secondCookie, "GET", path, 200), "First correction")
	two.Set("save_action_id", newOperationKey())
	two.Set("save_conflict_review", compositionField(t, conflict, "save_conflict_review"))
	itemPost(t, router, secondCookie, path, two, 303)
	verified := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", path, 200)
	assertContains(t, verified, "Second proposed correction")
	assertContains(t, verified, `<th>User</th><td>Recorded subject</td>`)
	assertContains(t, verified, "PRIVATE LINKED JOB")
	assertContains(t, verified, "51.234567, 12.345678")
}

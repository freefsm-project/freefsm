package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5/pgxpool"
)

func activityRepairPost(router http.Handler, cookie *http.Cookie, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func activityRepairCSV(t *testing.T, pool *pgxpool.Pool, router http.Handler, cookie *http.Cookie, filters url.Values) (string, [][]string) {
	t.Helper()
	values := cloneValues(filters)
	values.Set("scope", "A")
	values.Set("operation_id", newOperationKey())
	path := masterPost(t, router, cookie, "/activity/export", values, 303)
	if worked, err := exports.Product(pool).ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("prepare export: %v %v", worked, err)
	}
	rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return path, rows
}

func TestActivityRepairProvenanceAndOlderArtifactHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Provenance repair").SaveX(ctx)
	fieldPost(t, router, adminCookie, "/estimates", url.Values{"title": {"Original Estimate"}, "customer_id": {fmt.Sprint(c.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	e := client.Estimate.Query().OnlyX(ctx)
	source := fmt.Sprintf("/estimates/%d", e.ID)
	converted := fieldPost(t, router, adminCookie, source+"/convert", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	fieldPost(t, router, adminCookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	base := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "activity.export", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "No provenance", base)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("History reader").SetEmail("repair-provenance@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	filter := url.Values{"object_type": {"estimate"}, "object_id": {fmt.Sprint(e.ID)}, "action": {"estimate_converted"}}
	body := requestBody(t, router, cookie, "GET", "/activity?"+filter.Encode(), 200)
	assertNotContains(t, body, `class="activity-item activity-item-compact"`)
	_, rows := activityRepairCSV(t, pool, router, cookie, filter)
	if len(rows) != 1 {
		t.Fatal("provenance escaped parent-only export")
	}
	grants := append(append([]authorization.Grant{}, base...), authorization.Grant{Permission: "estimate.provenance.view", Scope: authorization.All})
	if _, err := auth.SaveRole(ctx, actor, role, "Provenance reader", grants, 1); err != nil {
		t.Fatal(err)
	}
	path, rows := activityRepairCSV(t, pool, router, cookie, filter)
	if len(rows) != 2 {
		t.Fatal("authorized provenance missing")
	}
	// Simulate the prior published adapter's incomplete reference. Current
	// action semantics, not a stored empty facets list, remain authoritative.
	if _, err := pool.Exec(ctx, `UPDATE export_records SET reference=reference-'facets' WHERE export_id=$1 AND ordinal>0`, strings.TrimPrefix(path, "/exports/")); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, actor, role, "Provenance revoked", base, 2); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", path+"/download", 410)
}

func TestActivityRepairFileEffectsAtomicOnceHTTP(t *testing.T) {
	for _, action := range []string{"upload", "rename", "delete"} {
		t.Run(action, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("File history").SaveX(ctx)
			operation := newOperationKey()
			const content = "FILE_BYTES_MUST_NOT_ENTER_HISTORY"
			var id, revision int64
			if action != "upload" {
				w := uploadFileHTTP(t, router, cookie, c.ID, "original.txt", []byte(content), operation)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				f := client.File.Query().OnlyX(ctx)
				id, revision = f.ID, f.Revision
			}
			before := ""
			if id > 0 {
				if err := pool.QueryRow(ctx, `SELECT to_jsonb(f)::text FROM files f WHERE id=$1`, id).Scan(&before); err != nil {
					t.Fatal(err)
				}
			}
			event := map[string]string{"upload": "file_uploaded", "rename": "file_renamed", "delete": "file_deleted"}[action]
			if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_file_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'File activity unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_file_history BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_file_history()`, event)); err != nil {
				t.Fatal(err)
			}
			send := func() *httptest.ResponseRecorder {
				if action == "upload" {
					return uploadFileHTTP(t, router, cookie, c.ID, "original.txt", []byte(content), operation)
				}
				return activityRepairPost(router, cookie, fmt.Sprintf("/files/%d/%s", id, action), url.Values{"original_name": {"renamed.txt"}, "revision": {fmt.Sprint(revision)}})
			}
			if w := send(); w.Code < 400 {
				t.Fatalf("history failure acknowledged: %d", w.Code)
			}
			if id == 0 {
				if client.File.Query().CountX(ctx) != 0 {
					t.Fatal("failed publication left a File")
				}
			} else {
				var after string
				if err := pool.QueryRow(ctx, `SELECT to_jsonb(f)::text FROM files f WHERE id=$1`, id).Scan(&after); err != nil || after != before {
					t.Fatal("failed File effect changed state", err)
				}
			}
			permission := map[string]string{"upload": "file.upload", "rename": "file.rename", "delete": "file.remove"}[action]
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='file' AND action=$1`, permission).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed effect retained success audit", count, err)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_file_history ON activity_logs`); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if w := send(); w.Code != 200 {
					t.Fatalf("effect/replay %d: %d %s", attempt, w.Code, w.Body.String())
				}
			}
			filter := url.Values{"object_type": {"customer"}, "object_id": {fmt.Sprint(c.ID)}, "action": {event}}
			_, rows := activityRepairCSV(t, pool, router, cookie, filter)
			if len(rows) != 2 {
				t.Fatalf("effect not once: %v", rows)
			}
			if !strings.Contains(fmt.Sprint(rows), ".txt") || strings.Contains(fmt.Sprint(rows), content) {
				t.Fatal("unsafe/missing File summary")
			}
			if action == "rename" && (!strings.Contains(rows[1][9], "original.txt") || !strings.Contains(rows[1][9], "renamed.txt")) {
				t.Fatal("rename before/after missing")
			}
		})
	}
}

func TestActivityRepairCustomerCascadeAndRestoreAtomicHTTP(t *testing.T) {
	for _, reject := range []string{"contact", "location", "asset", "customer", "restore"} {
		t.Run(reject, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cascade history").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			masterPost(t, router, cookie, base+"/contacts", url.Values{"first_name": {"Primary contact"}, "is_primary": {"true"}}, 303)
			masterPost(t, router, cookie, base+"/locations", url.Values{"title": {"Primary location"}, "is_primary": {"true"}}, 303)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Cascade Type").SaveX(ctx)
			client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Owned Asset").SetOwnershipClass("customer_owned").SaveX(ctx)
			var preview authorization.CustomerArchivePreview
			masterJSON(t, router, cookie, base+"/archive", &preview)
			event, kind, path := "archived", reject, base+"/delete"
			values := url.Values{"archive_snapshot": {preview.Snapshot}}
			if reject == "restore" {
				masterPost(t, router, cookie, path, values, 303)
				event, kind, path = "restored", "customer", base+"/restore"
				values = nil
			}
			snapshot := func() string {
				var s string
				if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('customer',(SELECT jsonb_agg(to_jsonb(x)) FROM customers x),'contacts',(SELECT jsonb_agg(to_jsonb(x)) FROM customer_contacts x),'locations',(SELECT jsonb_agg(to_jsonb(x)) FROM locations x),'assets',(SELECT jsonb_agg(to_jsonb(x)) FROM assets x),'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_audit x),'activity',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM activity_logs x))::text`).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := snapshot()
			if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_cascade_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.object_type='%s' AND NEW.action='%s' THEN RAISE EXCEPTION 'cascade history unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cascade_history BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_cascade_history()`, kind, event)); err != nil {
				t.Fatal(err)
			}
			if w := activityRepairPost(router, cookie, path, values); w.Code < 400 {
				t.Fatal("failed cascade acknowledged", w.Code)
			}
			if after := snapshot(); before != after {
				t.Fatal("cascade/restore left partial state, audit or history")
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_cascade_history ON activity_logs`); err != nil {
				t.Fatal(err)
			}
			masterPost(t, router, cookie, path, values, 303)
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action=$1`, event).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 4
			if reject == "restore" {
				want = 1
			}
			if n != want {
				t.Fatalf("expected exactly %d lifecycle events, got %d", want, n)
			}
		})
	}
}

func TestActivityRepairAssetWarrantyInstantHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Warranty Type").SaveX(ctx)
	fieldPost(t, router, cookie, "/assets", url.Values{"name": {"Warranty Asset"}, "ownership_class": {"company_owned"}, "asset_type_id": {fmt.Sprint(typ.ID)}, "warranty_expires": {"2027-01-15"}}, 303)
	a := client.Asset.Query().OnlyX(ctx)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d", a.ID), url.Values{"warranty_expires": {"2028-02-16"}}, 303)
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT metadata->'before'->>'warranty_expires',metadata->'after'->>'warranty_expires' FROM activity_logs WHERE object_type='asset' AND object_id=$1 AND action='updated'`, a.ID).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, after)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := time.Parse("2006-01-02", "2028-02-16")
	if !stamp.Equal(want) {
		t.Fatalf("wrong warranty instant: %s", after)
	}
	filter := url.Values{"object_type": {"asset"}, "object_id": {fmt.Sprint(a.ID)}, "action": {"updated"}}
	body := requestBody(t, router, cookie, "GET", "/activity?"+filter.Encode(), 200)
	assertContains(t, body, "warranty_expires")
	_, rows := activityRepairCSV(t, pool, router, cookie, filter)
	if len(rows) != 2 {
		t.Fatal("missing warranty event")
	}
	var facts []authorization.ActivityFact
	if err := json.Unmarshal([]byte(rows[1][9]), &facts); err != nil {
		t.Fatal(err)
	}
	for _, fact := range facts {
		if fact.Field == "warranty_expires" && fact.BeforeKnown && fact.AfterKnown && fact.Before == before && fact.After == after {
			return
		}
	}
	t.Fatal("warranty facts changed or disappeared")
}

func TestActivityRepairProjectLifecycleAtomicHTTP(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprint(restore), func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Project lifecycle").SaveX(ctx)
			var status int64
			if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed' LIMIT 1`, company).Scan(&status); err != nil {
				t.Fatal(err)
			}
			p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Completed Project").SetStatusID(status).SaveX(ctx)
			base := fmt.Sprintf("/projects/%d", p.ID)
			event, suffix := "archived", "/delete"
			if restore {
				masterPost(t, router, cookie, base+suffix, nil, 303)
				event, suffix = "restored", "/restore"
			}
			var before string
			if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM projects p WHERE id=$1`, p.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_project_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.object_type='project' AND NEW.action='%s' THEN RAISE EXCEPTION 'project history unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_project_history BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_project_history()`, event)); err != nil {
				t.Fatal(err)
			}
			if w := activityRepairPost(router, cookie, base+suffix, nil); w.Code < 400 {
				t.Fatal("history failure acknowledged")
			}
			var after string
			if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM projects p WHERE id=$1`, p.ID).Scan(&after); err != nil || before != after {
				t.Fatal("Project lifecycle escaped failed history", err)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_project_history ON activity_logs`); err != nil {
				t.Fatal(err)
			}
			masterPost(t, router, cookie, base+suffix, nil, 303)
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='project' AND object_id=$1 AND action=$2`, p.ID, event).Scan(&n); err != nil || n != 1 {
				t.Fatal("Project history duplicated/missing", n, err)
			}
		})
	}
}

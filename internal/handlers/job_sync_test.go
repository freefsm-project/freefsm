package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
)

type jobSyncResponse struct {
	Records   []map[string]any `json:"records"`
	Removed   []string         `json:"removed"`
	Next      *string          `json:"next_page"`
	Cursor    *string          `json:"completion_cursor"`
	Watermark string           `json:"watermark"`
	Complete  bool             `json:"complete"`
}

func syncFixture(t *testing.T, n int) (*associationRepairFixture, http.Handler, string, int64, int64, []int64) {
	t.Helper()
	f := associationRepair(t)
	ctx := context.Background()
	uid, role, _ := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "contact.view", Scope: authorization.Context}, {Permission: "location.view", Scope: authorization.Context}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "project.view", Scope: authorization.Context}})
	c := f.client.Customer.Create().SetID(9007199254740993).SetCompanyID(f.company).SetDisplayName("Closed Customer").SetNotes("PRIVATE-INTERNAL").SetServiceNotes("Allowed service").SaveX(ctx)
	ids := []int64{}
	for i := 0; i < n; i++ {
		j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType(fmt.Sprintf("Job %d", i)).SaveX(ctx)
		ids = append(ids, j.ID)
		f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	}
	token, err := f.sessions.CreateMobile(ctx, uid, "sync")
	if err != nil {
		t.Fatal(err)
	}
	return f, apiv1.NewRouter(f.pool, f.client, f.sessions), token, uid, role, ids
}

func TestJobSyncInstalledIdentityPrecision(t *testing.T) {
	f, api, token, _, _, _ := syncFixture(t, 1)
	page := syncGet(t, api, token, "", 200)
	customer := page.Records[0]["customer"].(map[string]any)
	var installed int64
	if err := f.pool.QueryRow(context.Background(), `SELECT installed_id FROM job_sync_identities WHERE opaque_id=$1`, customer["id"]).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if installed != 9007199254740993 {
		t.Fatalf("installed identity rounded: %d", installed)
	}
	if len(customer) != 9 || customer["display_name"] != "Closed Customer" || strings.Contains(fmt.Sprint(page), "PRIVATE-") {
		t.Fatalf("closed customer allowlist: %v", customer)
	}
}

func TestJobSyncScopeLossAndReplay(t *testing.T) {
	f, api, token, uid, _, ids := syncFixture(t, 3)
	ctx := context.Background()
	first := syncGet(t, api, token, "?page_size=1", 200)
	lost := first.Records[0]["id"].(string)
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
		t.Fatal(err)
	}
	second := syncGet(t, api, token, "?page_size=1&page="+*first.Next, 200)
	if !reflect.DeepEqual(second.Removed, []string{lost}) {
		t.Fatalf("previous-page scope loss not purged: %+v", second)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, ids[2]); err != nil {
		t.Fatal(err)
	}
	last := syncGet(t, api, token, "?page_size=1&page="+*second.Next, 200)
	if len(last.Records) != 0 || len(last.Removed) != 2 || !last.Complete {
		t.Fatalf("inaccessible page disclosed: %+v", last)
	}
	delta := syncGet(t, api, token, "?page_size=1&cursor="+*last.Cursor, 200)
	if len(delta.Records) != 0 || len(delta.Removed) != 2 || !delta.Complete {
		t.Fatalf("delta removals missing: %+v", delta)
	}
	replay := syncGet(t, api, token, "?page_size=1&cursor="+*last.Cursor, 200)
	if !reflect.DeepEqual(delta, replay) {
		t.Fatal("delta replay not deterministic")
	}
}

func TestJobSyncTokensFiltersExpiryAndBounds(t *testing.T) {
	f, api, token, uid, _, _ := syncFixture(t, 51)
	first := syncGet(t, api, token, "", 200)
	if len(first.Records) != 50 || first.Next == nil {
		t.Fatal("default page size")
	}
	for _, query := range []string{"?expand=customer", "?page_size=201", "?page_size=0", "?scope=assigned&scope=assigned", "?cursor=x&page=x", "?scope=unknown", "?%broken=1"} {
		syncGet(t, api, token, query, 400)
	}
	syncGet(t, api, token, "?page_size=1&page="+*first.Next, 409)
	other, err := f.sessions.CreateMobile(context.Background(), uid, "other device")
	if err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, other, "?page="+*first.Next, 409)
	if _, err = f.pool.Exec(context.Background(), `UPDATE job_sync_snapshots SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, token, "?page="+*first.Next, 409)
	max := syncGet(t, api, other, "?page_size=200", 200)
	if len(max.Records) != 51 || !max.Complete {
		t.Fatal("max page size")
	}
}

func TestJobSyncGrantAndRelationshipLoss(t *testing.T) {
	for _, change := range []string{"grant", "link"} {
		t.Run(change, func(t *testing.T) {
			f, api, token, _, role, ids := syncFixture(t, 2)
			ctx := context.Background()
			first := syncGet(t, api, token, "?page_size=1", 200)
			if change == "grant" {
				_, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, role)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Replacement").SaveX(ctx)
				if _, err := f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, c.ID, ids[0]); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest("GET", "/sync/jobs?page_size=1&page="+*first.Next, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 409 || !strings.Contains(w.Body.String(), `"recovery_disposition":"purge_and_resnapshot"`) || !strings.Contains(w.Body.String(), `"purge":"all"`) || strings.Contains(w.Body.String(), "Closed Customer") {
				t.Fatalf("unsafe scope response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestJobSyncStorageRollback(t *testing.T) {
	f, api, token, _, _, _ := syncFixture(t, 2)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_sync_token() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected token storage failure'; END $$; CREATE TRIGGER fail_sync_token BEFORE INSERT ON job_sync_tokens FOR EACH ROW EXECUTE FUNCTION fail_sync_token()`); err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, token, "?page_size=1", 503)
	for _, table := range []string{"job_sync_tokens", "job_sync_snapshots", "job_sync_identities"} {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("partial protocol state in %s: %d", table, n)
		}
	}
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER fail_sync_token ON job_sync_tokens`); err != nil {
		t.Fatal(err)
	}
	page := syncGet(t, api, token, "?page_size=1", 200)
	if page.Complete || page.Cursor != nil || page.Next == nil {
		t.Fatal("false completion")
	}
}

func TestJobSyncWaitingAuthority(t *testing.T) {
	f, api, token, _, role, _ := syncFixture(t, 1)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", "/sync/jobs", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		done <- w
	}()
	waitCredentialBlock(t, f.pool, pid)
	if _, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 || strings.Contains(w.Body.String(), "Closed Customer") {
			t.Fatalf("stale authority: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sync remained blocked")
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM job_sync_snapshots`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unauthorized snapshot materialized")
	}
}

func TestJobSyncClosedMastersAndArchivedProject(t *testing.T) {
	f, api, token, _, _, ids := syncFixture(t, 1)
	ctx := context.Background()
	const customer int64 = 9007199254740993
	contact := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(customer).SetFirstName("Explicit").SetLastName("Contact").SaveX(ctx)
	location := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(customer).SetTitle("Service site").SetAddress1("Main street").SaveX(ctx)
	project := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(customer).SetName("Retained Project").SetDescription("PRIVATE-ARCHIVED").SetServiceNotes("PRIVATE-PROJECT").SaveX(ctx)
	asset := f.asset(t, customer, "SYNC-1")
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET project_id=$1,customer_contact_id=$2,location_id=$3 WHERE id=$4`, project.ID, contact.ID, location.ID, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE projects SET deleted_at=clock_timestamp() WHERE id=$1`, project.ID); err != nil {
		t.Fatal(err)
	}
	f.post(t, f.cookie, fmt.Sprintf("/jobs/%d/assets", ids[0]), url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(asset.ID)}}, 303)
	page := syncGet(t, api, token, "", 200)
	m := page.Records[0]["masters"].(map[string]any)
	if !reflect.DeepEqual(m["project_reference"], map[string]any{"name": "Retained Project", "archived": true}) || m["project"] != nil || strings.Contains(fmt.Sprint(page), "PRIVATE-") {
		t.Fatalf("archived detail leak: %v", m)
	}
	keys := func(value any, want string) {
		t.Helper()
		object := value.(map[string]any)
		got := []string{}
		for key := range object {
			got = append(got, key)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != want {
			t.Fatalf("allowlist: %v", got)
		}
	}
	keys(m["contact"], "email,id,name,phone,role_title,service_notes")
	keys(m["location"], "access_notes,address_1,address_2,city,id,latitude,longitude,service_notes,state,title,zip_code")
	keys(m["asset"], "id,installed_at,location,manufacturer,model,name,number,ownership_class,serial_number,service_notes,status,type,warranty_expires")
}

func TestJobSyncAdditiveUpgrade(t *testing.T) {
	source := database.MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() != "078_job_sync.up.sql" {
			b, err := fs.ReadFile(source, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "installed")
	if err != nil {
		t.Fatal(err)
	}
	inventory := func() string {
		t.Helper()
		var raw string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),'grants',(SELECT jsonb_agg(to_jsonb(x) ORDER BY role_id,permission,scope) FROM authorization_grants x),'customers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM customers x),'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM jobs x),'assignments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job_assignments x),'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_audit x))::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := inventory()
	db := &database.DB{Pool: pool}
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
		if inventory() != before {
			t.Fatal("migration rewrote installed facts")
		}
	}
	page := syncGet(t, apiv1.NewRouter(pool, client, sessions), token, "", 200)
	if len(page.Records) != 1 || page.Records[0]["title"] != "Installed Job" {
		t.Fatalf("installed job lost: %+v", page)
	}
	if inventory() != before {
		t.Fatal("sync fabricated domain history")
	}
}

func TestJobSyncConcurrentDeltaReplayAndRetention(t *testing.T) {
	f, api, token, _, _, ids := syncFixture(t, 1)
	ctx := context.Background()
	first := syncGet(t, api, token, "", 200)
	// Move the actual retained baseline to 29 days old with its full 30-day
	// lifetime, then reconstruct the production router (no in-memory cursors).
	if _, err := f.pool.Exec(ctx, `UPDATE job_sync_snapshots SET watermark=clock_timestamp()-interval '29 days',expires_at=clock_timestamp()+interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET job_type='Concurrent change' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	api = apiv1.NewRouter(f.pool, f.client, f.sessions)
	ready := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-ready
			r := httptest.NewRequest("GET", "/sync/jobs?cursor="+*first.Cursor, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			done <- w
		}()
	}
	close(ready)
	a, b := <-done, <-done
	if a.Code != 200 || b.Code != 200 || a.Body.String() != b.Body.String() {
		t.Fatalf("concurrent cursor divergence: %d %s / %d %s", a.Code, a.Body.String(), b.Code, b.Body.String())
	}
	var envelope struct {
		Data jobSyncResponse `json:"data"`
	}
	if err := json.Unmarshal(a.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Records[0]["title"] != "Concurrent change" {
		t.Fatal("delta lost edit")
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM job_sync_snapshots WHERE previous_cursor=$1`, *first.Cursor).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("duplicate successor")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET job_type='Later change' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	next := syncGet(t, api, token, "?cursor="+*envelope.Data.Cursor, 200)
	if next.Records[0]["title"] != "Later change" || next.Records[0]["id"] != first.Records[0]["id"] {
		t.Fatal("successor failed to catch subsequent edit")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE job_sync_snapshots SET watermark=clock_timestamp()-interval '31 days',expires_at=clock_timestamp()-interval '1 day' WHERE id=(SELECT snapshot_id FROM job_sync_tokens WHERE token=$1)`, *first.Cursor); err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, token, "?cursor="+*first.Cursor, 409)
}

func TestJobSyncUnchangedNumericContext(t *testing.T) {
	f, api, token, _, _, ids := syncFixture(t, 1)
	ctx := context.Background()
	const customer int64 = 9007199254740993
	location := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(customer).SetTitle("Numeric site").SetLatitude(35.125).SetLongitude(-80.25).SaveX(ctx)
	project := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(customer).SetName("Progress").SetCompletionPercentage(37).SaveX(ctx)
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET location_id=$1,project_id=$2 WHERE id=$3`, location.ID, project.ID, ids[0]); err != nil {
		t.Fatal(err)
	}
	first := syncGet(t, api, token, "", 200)
	next := syncGet(t, api, token, "?cursor="+*first.Cursor, 200)
	if len(next.Records) != 0 || len(next.Removed) != 0 || !next.Complete {
		t.Fatalf("fabricated unchanged delta: %+v", next)
	}
}

func syncGet(t *testing.T, api http.Handler, token, query string, want int) jobSyncResponse {
	t.Helper()
	r := httptest.NewRequest("GET", "/sync/jobs"+query, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("sync %s: %d want %d: %s", query, w.Code, want, w.Body.String())
	}
	var envelope struct {
		Data jobSyncResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestJobSyncCoherentPages(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	uid, _, _ := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}})
	customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Closed Customer").SetNotes("PRIVATE-BILLING").SaveX(ctx)
	var ids []int64
	for i := 0; i < 3; i++ {
		j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetJobType(fmt.Sprintf("Original %d", i)).SaveX(ctx)
		ids = append(ids, j.ID)
		f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	}
	token, err := f.sessions.CreateMobile(ctx, uid, "sync")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	first := syncGet(t, api, token, "?page_size=1", 200)
	if len(first.Records) != 1 || first.Next == nil || first.Cursor != nil || first.Complete || first.Watermark == "" {
		t.Fatalf("invalid first page: %+v", first)
	}
	opaque, ok := first.Records[0]["id"].(string)
	if !ok || opaque == fmt.Sprint(ids[0]) {
		t.Fatal("nonopaque identity")
	}
	if _, err = f.pool.Exec(ctx, `UPDATE jobs SET job_type='Changed during paging' WHERE id=$1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	added := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetJobType("Added during paging").SaveX(ctx)
	f.client.JobAssignment.Create().SetJobID(added.ID).SetUserID(uid).SaveX(ctx)
	second := syncGet(t, api, token, "?page_size=1&page="+url.QueryEscape(*first.Next), 200)
	replay := syncGet(t, api, token, "?page_size=1&page="+url.QueryEscape(*first.Next), 200)
	if !reflect.DeepEqual(second, replay) || second.Records[0]["title"] != "Original 1" {
		t.Fatalf("unstable page: %+v / %+v", second, replay)
	}
	last := syncGet(t, api, token, "?page_size=1&page="+url.QueryEscape(*second.Next), 200)
	if !last.Complete || last.Cursor == nil || last.Next != nil {
		t.Fatalf("invalid completion: %+v", last)
	}
	delta := syncGet(t, api, token, "?page_size=1&cursor="+url.QueryEscape(*last.Cursor), 200)
	if len(delta.Records) != 1 || delta.Records[0]["title"] != "Changed during paging" {
		t.Fatalf("lost concurrent edit: %+v", delta)
	}
	end := syncGet(t, api, token, "?page_size=1&page="+url.QueryEscape(*delta.Next), 200)
	if !end.Complete || end.Records[0]["title"] != "Added during paging" {
		t.Fatalf("lost concurrent insert: %+v", end)
	}
}

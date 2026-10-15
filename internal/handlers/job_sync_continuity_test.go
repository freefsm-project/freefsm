package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
)

func continuityRequest(api http.Handler, token, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/sync/jobs"+query, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func continuityResnapshot(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"resnapshot_required"`) || !strings.Contains(w.Body.String(), `"recovery_disposition":"purge_and_resnapshot"`) || !strings.Contains(w.Body.String(), `"purge":"all"`) || strings.Contains(w.Body.String(), "Closed Customer") {
		t.Fatalf("unsafe continuity: %d %s", w.Code, w.Body.String())
	}
}

func TestJobSyncContinuityUnchangedReassignment(t *testing.T) {
	for _, application := range []string{"applied", "lost-response", "local-application-failed"} {
		t.Run(application, func(t *testing.T) {
			f, api, token, uid, _, ids := syncFixture(t, 2)
			ctx := context.Background()
			first := syncGet(t, api, token, "?page_size=1", 200)
			query := "?page_size=1&page=" + *first.Next
			cache := map[string]map[string]any{first.Records[0]["id"].(string): first.Records[0]}
			if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
				t.Fatal(err)
			}
			loss := continuityRequest(api, token, query)
			// Accept the former removal response here so RED demonstrates the
			// actual unchanged-reassignment failure, not just a status change.
			if loss.Code == 200 {
				var envelope struct {
					Data jobSyncResponse `json:"data"`
				}
				if err := json.Unmarshal(loss.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(envelope.Data.Removed, []string{first.Records[0]["id"].(string)}) {
					t.Fatal("missing authoritative purge")
				}
				if application != "lost-response" {
					for _, id := range envelope.Data.Removed {
						delete(cache, id)
					}
				}
				if application == "applied" {
					for _, record := range envelope.Data.Records {
						cache[record["id"].(string)] = record
					}
				}
			} else {
				continuityResnapshot(t, loss)
				if application != "lost-response" {
					clear(cache)
				}
			}
			f.client.JobAssignment.Create().SetJobID(ids[0]).SetUserID(uid).SaveX(ctx)
			api = apiv1.NewRouter(f.pool, f.client, f.sessions)
			// Even a lost response or a purge followed by failed local storage
			// cannot make this same page valid again when A is restored unchanged.
			continuityResnapshot(t, continuityRequest(api, token, query))
			clear(cache)
			fresh := syncGet(t, api, token, "", 200)
			for _, record := range fresh.Records {
				cache[record["id"].(string)] = record
			}
			if len(cache) != 2 || !reflect.DeepEqual(cache[first.Records[0]["id"].(string)], first.Records[0]) {
				t.Fatal("fresh authoritative replacement did not restore unchanged A")
			}
			continuityResnapshot(t, continuityRequest(api, token, query))
			delta := syncGet(t, api, token, "?cursor="+*fresh.Cursor, 200)
			if len(delta.Records) != 0 || len(delta.Removed) != 0 {
				t.Fatal("old replay damaged fresh continuity")
			}
		})
	}
}

func TestJobSyncContinuityInvalidatesExistingSuccessors(t *testing.T) {
	f, api, token, uid, _, ids := syncFixture(t, 2)
	ctx := context.Background()
	first := syncGet(t, api, token, "?page_size=1", 200)
	last := syncGet(t, api, token, "?page_size=1&page="+*first.Next, 200)
	child := syncGet(t, api, token, "?page_size=1&cursor="+*last.Cursor, 200)
	grandchild := syncGet(t, api, token, "?page_size=1&cursor="+*child.Cursor, 200)
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
		t.Fatal(err)
	}
	query := "?page_size=1&page=" + *first.Next
	ready := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() { <-ready; done <- continuityRequest(api, token, query) }()
	}
	close(ready)
	a, b := <-done, <-done
	// Read both before failing to keep fixture teardown out of concurrent work.
	continuityResnapshot(t, a)
	continuityResnapshot(t, b)
	f.client.JobAssignment.Create().SetJobID(ids[0]).SetUserID(uid).SaveX(ctx)
	api = apiv1.NewRouter(f.pool, f.client, f.sessions)
	for _, q := range []string{query, "?page_size=1&cursor=" + *last.Cursor, "?page_size=1&cursor=" + *child.Cursor, "?page_size=1&cursor=" + *grandchild.Cursor} {
		continuityResnapshot(t, continuityRequest(api, token, q))
	}
	fresh := syncGet(t, api, token, "", 200)
	if len(fresh.Records) != 2 {
		t.Fatal("reassignment absent from fresh snapshot")
	}
}

func TestJobSyncContinuityDeltaRemoval(t *testing.T) {
	f, api, token, uid, _, ids := syncFixture(t, 1)
	ctx := context.Background()
	first := syncGet(t, api, token, "", 200)
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
		t.Fatal(err)
	}
	query := "?cursor=" + *first.Cursor
	continuityResnapshot(t, continuityRequest(api, token, query))
	f.client.JobAssignment.Create().SetJobID(ids[0]).SetUserID(uid).SaveX(ctx)
	continuityResnapshot(t, continuityRequest(apiv1.NewRouter(f.pool, f.client, f.sessions), token, query))
}

// Exercise the actual cache hole rather than requiring a particular loss status:
// old servers complete with a purge then omit unchanged A forever; repaired
// servers require authoritative replacement before continuity can resume.
func TestJobSyncContinuityCacheRestoresUnchangedJob(t *testing.T) {
	for _, successors := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion-after-loss", true: "nested-existing-successor"}[successors], func(t *testing.T) {
			f, api, token, uid, _, ids := syncFixture(t, 2)
			ctx := context.Background()
			first := syncGet(t, api, token, "?page_size=1", 200)
			lost := first.Records[0]["id"].(string)
			cache := map[string]map[string]any{lost: first.Records[0]}
			var cursor *string
			if successors {
				last := syncGet(t, api, token, "?page_size=1&page="+*first.Next, 200)
				child := syncGet(t, api, token, "?page_size=1&cursor="+*last.Cursor, 200)
				grandchild := syncGet(t, api, token, "?page_size=1&cursor="+*child.Cursor, 200)
				cursor = grandchild.Cursor
			}
			if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
				t.Fatal(err)
			}
			loss := continuityRequest(api, token, "?page_size=1&page="+*first.Next)
			if loss.Code == 200 {
				var envelope struct {
					Data jobSyncResponse `json:"data"`
				}
				if err := json.Unmarshal(loss.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				for _, id := range envelope.Data.Removed {
					delete(cache, id)
				}
				for _, record := range envelope.Data.Records {
					cache[record["id"].(string)] = record
				}
				if !successors {
					cursor = envelope.Data.Cursor
				}
			} else {
				continuityResnapshot(t, loss)
				clear(cache)
			}
			if _, ok := cache[lost]; ok {
				t.Fatal("loss failed to purge A")
			}
			f.client.JobAssignment.Create().SetJobID(ids[0]).SetUserID(uid).SaveX(ctx)
			api = apiv1.NewRouter(f.pool, f.client, f.sessions)
			if cursor != nil {
				w := continuityRequest(api, token, "?page_size=1&cursor="+*cursor)
				if w.Code == 200 {
					var envelope struct {
						Data jobSyncResponse `json:"data"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
						t.Fatal(err)
					}
					for _, record := range envelope.Data.Records {
						cache[record["id"].(string)] = record
					}
					if _, ok := cache[lost]; !ok {
						t.Fatal("authorized unchanged A silently omitted after delivered purge and reassignment")
					}
					return
				}
				continuityResnapshot(t, w)
			}
			clear(cache)
			fresh := syncGet(t, api, token, "", 200)
			for _, record := range fresh.Records {
				cache[record["id"].(string)] = record
			}
			if !reflect.DeepEqual(cache[lost], first.Records[0]) {
				t.Fatal("authoritative recovery did not restore unchanged A")
			}
		})
	}
}

func TestJobSyncContinuityForbiddenThenRestored(t *testing.T) {
	for _, scope := range []string{"assigned", "all"} {
		t.Run(scope, func(t *testing.T) {
			f, api, token, _, role, _ := syncFixture(t, 1)
			ctx := context.Background()
			if scope == "all" {
				if _, err := f.pool.Exec(ctx, `UPDATE authorization_grants SET scope='A' WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
					t.Fatal(err)
				}
			}
			first := syncGet(t, api, token, "?scope="+scope, 200)
			if _, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
				t.Fatal(err)
			}
			if scope == "all" {
				if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'job.view','J')`, role); err != nil {
					t.Fatal(err)
				}
			}
			query := "?scope=" + scope + "&cursor=" + *first.Cursor
			syncGet(t, api, token, query, 403)
			grant := "J"
			if scope == "all" {
				grant = "A"
			}
			if _, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'job.view',$2)`, role, grant); err != nil {
				t.Fatal(err)
			}
			continuityResnapshot(t, continuityRequest(apiv1.NewRouter(f.pool, f.client, f.sessions), token, query))
		})
	}
}

func TestJobSyncContinuityInvalidationStorageFailure(t *testing.T) {
	for _, failure := range []string{"statement", "commit"} {
		t.Run(failure, func(t *testing.T) {
			f, api, token, uid, _, ids := syncFixture(t, 2)
			ctx := context.Background()
			first := syncGet(t, api, token, "?page_size=1", 200)
			if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_sync_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected invalidation failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_sync_invalidation BEFORE UPDATE ON job_sync_snapshots FOR EACH ROW EXECUTE FUNCTION fail_sync_invalidation()`
			if failure == "commit" {
				trigger = `CREATE CONSTRAINT TRIGGER fail_sync_invalidation AFTER UPDATE ON job_sync_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_sync_invalidation()`
			}
			if _, err := f.pool.Exec(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			query := "?page_size=1&page=" + *first.Next
			w := continuityRequest(api, token, query)
			if w.Code != 503 || strings.Contains(w.Body.String(), "completion_cursor") || strings.Contains(w.Body.String(), "purge_and_resnapshot") {
				t.Fatalf("false committed purge: %d %s", w.Code, w.Body.String())
			}
			var invalid int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM job_sync_snapshots WHERE invalidated`).Scan(&invalid); err != nil {
				t.Fatal(err)
			}
			if invalid != 0 {
				t.Fatal("partial invalidation after rollback")
			}
			if _, err := f.pool.Exec(ctx, `DROP TRIGGER fail_sync_invalidation ON job_sync_snapshots`); err != nil {
				t.Fatal(err)
			}
			continuityResnapshot(t, continuityRequest(api, token, query))
			f.client.JobAssignment.Create().SetJobID(ids[0]).SetUserID(uid).SaveX(ctx)
			continuityResnapshot(t, continuityRequest(apiv1.NewRouter(f.pool, f.client, f.sessions), token, query))
		})
	}
}

func TestJobSyncContinuityClientIsolation(t *testing.T) {
	f, api, token, uid, _, ids := syncFixture(t, 1)
	ctx := context.Background()
	other, err := f.sessions.CreateMobile(ctx, uid, "other continuity")
	if err != nil {
		t.Fatal(err)
	}
	a := syncGet(t, api, token, "", 200)
	b := syncGet(t, api, other, "", 200)
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, ids[0], uid); err != nil {
		t.Fatal(err)
	}
	continuityResnapshot(t, continuityRequest(api, token, "?cursor="+*a.Cursor))
	f.client.JobAssignment.Create().SetJobID(ids[0]).SetUserID(uid).SaveX(ctx)
	// This client never observed a purge and still has A; its continuity is safe.
	delta := syncGet(t, api, other, "?cursor="+*b.Cursor, 200)
	if len(delta.Records) != 0 || len(delta.Removed) != 0 {
		t.Fatal("unrelated client continuity changed")
	}
}

func TestJobSyncContinuityInstalled078Upgrade(t *testing.T) {
	source := database.MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() == "079_job_sync_continuity.up.sql" {
			continue
		}
		b, err := fs.ReadFile(source, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[e.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "installed continuity")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	service := &authorization.Service{Pool: pool}
	p, err := service.Principal(ctx, authorization.Actor{UserID: admin, SessionHash: hash, Channel: "api"})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(p.Grants)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(grants)
	// Actual installed078 protocol rows: the old implementation cannot record
	// whether this baseline's A was purged. Preserve the evidence, retire tokens.
	if _, err := pool.Exec(ctx, `INSERT INTO job_sync_identities(resource_type,installed_id,opaque_id) VALUES('job',$1,'installed-opaque-job')`, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_sync_snapshots(id,user_id,session_hash,epoch,scope,page_size,grants_hash,baseline,changes,removed) SELECT 'installed-snapshot',$1,$2,epoch::text,'assigned',50,$3,'[]','[]','[]' FROM session_instance WHERE singleton`, admin, hash, hex.EncodeToString(fingerprint[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_sync_tokens(token,snapshot_id,kind,position) VALUES('installed-cursor','installed-snapshot','cursor',0),('installed-page','installed-snapshot','page',0)`); err != nil {
		t.Fatal(err)
	}
	inventory := func() string {
		t.Helper()
		var raw string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('identities',(SELECT jsonb_agg(to_jsonb(x)) FROM job_sync_identities x),'tokens',(SELECT jsonb_agg(to_jsonb(x) ORDER BY token) FROM job_sync_tokens x),'snapshots',(SELECT jsonb_agg(to_jsonb(x)-'invalidated' ORDER BY id) FROM job_sync_snapshots x),'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM jobs x),'grants',(SELECT jsonb_agg(to_jsonb(x) ORDER BY role_id,permission,scope) FROM authorization_grants x),'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM authorization_audit x))::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := inventory()
	for i := 0; i < 2; i++ {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
		if inventory() != before {
			t.Fatal("upgrade rewrote installed domain/protocol evidence")
		}
	}
	api := apiv1.NewRouter(pool, client, sessions)
	continuityResnapshot(t, continuityRequest(api, token, "?cursor=installed-cursor"))
	continuityResnapshot(t, continuityRequest(api, token, "?page=installed-page"))
	fresh := syncGet(t, api, token, "", 200)
	if len(fresh.Records) != 1 || fresh.Records[0]["id"] != "installed-opaque-job" {
		t.Fatal("installed identity lost during recovery")
	}
	continuityResnapshot(t, continuityRequest(api, token, "?cursor=installed-cursor"))
	syncGet(t, api, token, "?cursor="+*fresh.Cursor, 200)
}

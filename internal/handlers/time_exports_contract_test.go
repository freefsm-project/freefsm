package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5/pgxpool"
)

func prepareTimeExport(t *testing.T, pool *pgxpool.Pool, router http.Handler, cookie *http.Cookie, values url.Values) string {
	t.Helper()
	values.Set("operation_id", newOperationKey())
	path := masterPost(t, router, cookie, "/time-entries/export", values, 303)
	if worked, err := exports.Product(pool).ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("worker: %v %v", worked, err)
	}
	return path
}

func timeExportCSV(t *testing.T, body string) []map[string]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) == 0 {
		t.Fatalf("CSV: %v %q", err, body)
	}
	result := []map[string]string{}
	for _, row := range rows[1:] {
		m := map[string]string{}
		for i, key := range rows[0] {
			m[key] = row[i]
		}
		result = append(result, m)
	}
	return result
}

func TestTimeExportScopesAndOriginalAnchorsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	worker := client.User.Create().SetCompanyID(company).SetEmail("time-export-worker@test").SetName("Export subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.export", Scope: authorization.Own}, {Permission: "time.export", Scope: authorization.Assigned}, {Permission: "job.view", Scope: authorization.Assigned}}
	role, err := auth.SaveRole(ctx, actor, 0, "Bounded time export", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, actor, worker.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE CUSTOMER").SaveX(ctx)
	old := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Retained title").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Assigned title").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(worker.ID).SaveX(ctx)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	own := client.TimeEntry.Create().SetCompanyID(company).SetUserID(worker.ID).SetJobID(old.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Own retained").SaveX(ctx)
	assigned := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetJobID(job.ID).SetClockIn(start).SetClockOut(start.Add(2 * time.Hour)).SetIsManual(true).SetNotes("Assigned manual").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start.Add(3 * time.Hour)).SetClockOut(start.Add(4 * time.Hour)).SetNotes("PRIVATE standalone").SaveX(ctx)
	client.Job.UpdateOne(old).SetJobType("PRIVATE renamed").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, worker.ID)
	ownPath := prepareTimeExport(t, pool, router, cookie, url.Values{"scope": {"O"}, "group_by": {"job"}})
	body := requestBody(t, router, cookie, "GET", ownPath+"/download", 200)
	rows := timeExportCSV(t, body)
	if len(rows) != 1 || rows[0]["entry_id"] != fmt.Sprint(own.ID) {
		t.Fatalf("Own scope widened: %v", rows)
	}
	assertContains(t, body, "Retained title")
	assertNotContains(t, body, "PRIVATE")
	assertNotContains(t, body, "Assigned manual")
	expectStatus(t, router, cookie, "GET", "/time-entries/export?scope=A", 403)
	expectStatus(t, router, cookie, "GET", "/time-entries/export?scope=J&type=gps", 403)
	assignedPath := prepareTimeExport(t, pool, router, cookie, url.Values{"scope": {"J"}, "source": {"manual"}, "lifecycle": {"closed"}, "group_by": {"job"}, "user_id": {fmt.Sprint(admin)}, "job_id": {fmt.Sprint(job.ID)}})
	rows = timeExportCSV(t, requestBody(t, router, cookie, "GET", assignedPath+"/download", 200))
	if len(rows) != 1 || rows[0]["entry_id"] != fmt.Sprint(assigned.ID) {
		t.Fatalf("Assigned filters: %v", rows)
	}
	// A changed current Job must not authorize the original artifact after loss
	// of its original assignment. Conversely Own survives loss and private rename.
	newJob := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Replacement assigned").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(newJob.ID).SetUserID(worker.ID).SaveX(ctx)
	if err := auth.CorrectTimeEntry(ctx, actor, assigned.ID, authorization.TimeEntryCorrection{Reason: "Reviewed link change", JobID: &newJob.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", assignedPath+"/download", 410)
	assertContains(t, requestBody(t, router, cookie, "GET", ownPath+"/download", 200), "Own retained")
}

func TestTimeExportAllMatchingAsOfAndHistoryHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	// More than every supported list page; microsecond remainders must survive.
	if _, err := pool.Exec(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,clock_out,notes,is_manual)
	 SELECT $1,$2,'2026-01-01 00:00Z'::timestamptz+n*interval '2 minutes','2026-01-01 00:00Z'::timestamptz+n*interval '2 minutes'+interval '1 minute 0.000001 second','Exact row '||n,true FROM generate_series(1,125) n`, company, admin); err != nil {
		t.Fatal(err)
	}
	path := prepareTimeExport(t, pool, router, cookie, url.Values{"source": {"manual"}, "sort": {"start_desc"}})
	rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
	if len(rows) != 125 {
		t.Fatalf("truncated: %d", len(rows))
	}
	var total int64
	for _, row := range rows {
		n, err := strconv.ParseInt(row["duration_microseconds"], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 125*60000001 || rows[0]["notes"] != "Exact row 125" {
		t.Fatalf("exact sorted total: %d %v", total, rows[0])
	}
	activeStart := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	active := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(activeStart).SetNotes("Provisional identity").SaveX(ctx)
	activePath := prepareTimeExport(t, pool, router, cookie, url.Values{"lifecycle": {"active"}})
	rows = timeExportCSV(t, requestBody(t, router, cookie, "GET", activePath+"/download", 200))
	var status exports.Status
	masterJSON(t, router, cookie, activePath, &status)
	var activeTotal int64
	for _, row := range rows {
		asOf, err := time.Parse(time.RFC3339Nano, row["as_of"])
		if err != nil {
			t.Fatal(err)
		}
		if status.AsOf == nil || !asOf.Equal(*status.AsOf) || row["clock_out"] != "" || row["provisional"] != "true" {
			t.Fatalf("mixed as-of: %+v %v", status, row)
		}
		n, err := strconv.ParseInt(row["duration_microseconds"], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		activeTotal += n
	}
	if activeTotal != status.AsOf.Sub(activeStart).Microseconds() {
		t.Fatalf("active total %d", activeTotal)
	}
	timePost(t, router, cookie, fmt.Sprintf("/time-entries/%d/void", active.ID), url.Values{"reason": {"Terminal without invented end"}}, 303)
	voidPath := prepareTimeExport(t, pool, router, cookie, url.Values{"include_void": {"true"}, "lifecycle": {"void"}})
	rows = timeExportCSV(t, requestBody(t, router, cookie, "GET", voidPath+"/download", 200))
	if len(rows) != 1 || rows[0]["clock_out"] != "" || rows[0]["duration_microseconds"] != "0" || rows[0]["provisional"] != "false" {
		t.Fatalf("Void facts: %v", rows)
	}
	assertContains(t, rows[0]["revisions"], "Terminal without invented end")
	assertContains(t, rows[0]["revisions"], "AcceptedAt")
	assertContains(t, rows[0]["revisions"], "ActorID")
}

func TestTimeExportRetentionFailuresAndAPIHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Retained export fact").SaveX(ctx)
	for _, event := range []string{"accepted", "generated", "downloaded"} {
		t.Run(event, func(t *testing.T) {
			if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_time_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='time.export' AND NEW.detail->>'event'='%s' THEN RAISE EXCEPTION 'injected export audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_time_export_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_time_export_audit()`, event)); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := pool.Exec(ctx, `DROP TRIGGER fail_time_export_audit ON authorization_audit;DROP FUNCTION fail_time_export_audit()`); err != nil {
					t.Error(err)
				}
			}()
			v := url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}
			if event == "accepted" {
				masterPost(t, router, cookie, "/time-entries/export", v, 409)
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs WHERE operation_id=$1`, v.Get("operation_id")).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial acceptance %d %v", count, err)
				}
				return
			}
			path := masterPost(t, router, cookie, "/time-entries/export", v, 303)
			_, err := exports.Product(pool).ProcessOne(ctx)
			if event == "generated" {
				if err == nil {
					t.Fatal("generation audit failure succeeded")
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_records r JOIN export_jobs j ON j.id=r.export_id WHERE j.operation_id=$1`, v.Get("operation_id")).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial artifact %d %v", count, err)
				}
				assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/download", 503), "Retained export fact")
			}
		})
	}
	path := prepareTimeExport(t, pool, router, cookie, url.Values{"scope": {"A"}})
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "Retained export fact")
	var generated, downloaded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE detail->>'event'='generated'),count(*) FILTER(WHERE detail->>'event'='downloaded') FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND action='time.export'`, strings.TrimPrefix(path, "/exports/")).Scan(&generated, &downloaded); err != nil || generated != 1 || downloaded != 1 {
		t.Fatalf("generation/download audit: %d %d %v", generated, downloaded, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/")); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	// Existing installed facts and grants survive repeated migration unchanged.
	var before, after string
	query := `SELECT jsonb_build_object('entries',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM time_entries e),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`
	if err := pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatalf("installed data changed: %v", err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "No field exports")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, method := range []string{"GET", "POST"} {
		for _, target := range []string{"/time-entries/export", "/exports", "/exports/1/download"} {
			r := httptest.NewRequest(method, target, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("field export expanded: %s %s %d", method, target, w.Code)
			}
		}
	}
}

func TestTimeExportGPSOwnAndAssignedScopesHTTP(t *testing.T) {
	for _, scope := range []authorization.Scope{authorization.Own, authorization.Assigned} {
		t.Run(string(scope), func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			actor := authorization.Actor{UserID: admin}
			auth := authorization.New(pool)
			worker := client.User.Create().SetCompanyID(company).SetEmail("scoped-gps@test").SetName("Scoped GPS subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
			grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.gps.view", Scope: authorization.All}, {Permission: "time.gps.export", Scope: scope}, {Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "time.gps.capture", Scope: authorization.Own}, {Permission: "job.view", Scope: authorization.Assigned}}
			role, err := auth.SaveRole(ctx, actor, 0, "Independent scoped GPS", grants)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.AssignUser(ctx, actor, worker.ID, role); err != nil {
				t.Fatal(err)
			}
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Scoped GPS Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Scoped GPS Job").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(job.ID).SetUserID(worker.ID).SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, worker.ID)
			captured := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
			itemPost(t, router, cookie, fmt.Sprintf("/jobs/%d/clock-in", job.ID), url.Values{"latitude": {"12.5"}, "longitude": {"23.5"}, "captured_at": {captured}}, 303)
			path := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {"gps"}, "scope": {string(scope)}})
			assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "12.5")
			if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, worker.ID); err != nil {
				t.Fatal(err)
			}
			if scope == authorization.Assigned {
				expectStatus(t, router, cookie, "GET", path+"/download", 410)
			} else {
				assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "Scoped GPS Job")
			}
		})
	}
}

func TestTimeExportGPSAbsenceAndOptionalAccuracyHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	// Legacy coordinates are recorded facts, not accepted clock observations.
	if _, err := pool.Exec(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,clock_out,latitude,longitude,notes) VALUES($1,$2,'2026-01-01 10:00Z','2026-01-01 11:00Z',45,90,'Legacy coordinates without capture')`, company, admin); err != nil {
		t.Fatal(err)
	}
	empty := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {"gps"}})
	if rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", empty+"/download", 200)); len(rows) != 0 {
		t.Fatalf("fabricated legacy GPS: %v", rows)
	}
	captured := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	itemPost(t, router, cookie, "/time-entries/clock-in", url.Values{"latitude": {"0"}, "longitude": {"0"}, "captured_at": {captured}}, 303)
	path := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {"gps"}, "lifecycle": {"active"}})
	rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
	if len(rows) != 1 || rows[0]["latitude"] != "0" || rows[0]["longitude"] != "0" || rows[0]["accuracy"] != "" || rows[0]["captured_at"] != captured {
		t.Fatalf("actual zero pair or omitted accuracy changed: %v", rows)
	}
	var activeID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM time_entries WHERE user_id=$1 AND clock_out IS NULL`, admin).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, "/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(activeID)}}, 303)
	all := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {"gps"}})
	if rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", all+"/download", 200)); len(rows) != 1 {
		t.Fatalf("location-free out fabricated an observation: %v", rows)
	}
	for _, q := range []string{"source=invalid", "lifecycle=invalid", "group_by=invalid", "sort=invalid", "date_from=2026-02-30", "date_from=2026-02-02&date_to=2026-01-01", "include_void=yes", "job_id=0", "user_id=-1", "type=unknown"} {
		expectStatus(t, router, cookie, "GET", "/time-entries/export?"+q, 400)
	}
}

func TestTimeExportLongDSTDayHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2025, 11, 2, 4, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(25 * time.Hour)).SaveX(ctx)
	path := prepareTimeExport(t, pool, router, cookie, url.Values{"date_from": {"2025-11-02"}, "date_to": {"2025-11-02"}})
	rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
	if len(rows) != 1 || rows[0]["duration_microseconds"] != "90000000000" || rows[0]["company_date"] != "2025-11-02" {
		t.Fatalf("long day: %v", rows)
	}
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(e)::text FROM time_entries e WHERE id=$1`, entry.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC'`); err != nil {
		t.Fatal(err)
	}
	newPath := prepareTimeExport(t, pool, router, cookie, url.Values{})
	if len(timeExportCSV(t, requestBody(t, router, cookie, "GET", newPath+"/download", 200))) != 2 {
		t.Fatal("timezone did not change presentation boundaries")
	}
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(e)::text FROM time_entries e WHERE id=$1`, entry.ID).Scan(&after); err != nil || before != after {
		t.Fatalf("timezone rewrote entry: %v", err)
	}
}

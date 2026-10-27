package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestJobArchiveOwnClockAndFreshFieldIntentAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Archive field worker", []authorization.Grant{
		{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned},
		{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own},
		{Permission: "time.clock_out", Scope: authorization.Own}, {Permission: "time.gps.capture", Scope: authorization.Own},
	})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Field worker").SetEmail("archive-clock@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE CUSTOMER").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&status))
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Retained title").SetNotes("PRIVATE PLANNING").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Archive clock reference")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	pending := executionNotes(t, api, token, path, "Pending before archive")
	accepted := clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "job_id": j.ID}, 200)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	before := historyDurableRows(t, pool, []string{"time_entries", "time_entry_history"})
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
	historyEqual(t, "archive does not stop clocks", historyDurableRows(t, pool, []string{"time_entries", "time_entry_history"}), before)
	executionAPI(t, api, token, "GET", path, nil, 404)
	executionAPI(t, api, token, "POST", path+"/field-notes", pending, 404)
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": accepted["entry_id"], "gps": map[string]any{"latitude": 51.5, "longitude": -0.12, "accuracy": 4, "captured_at": time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)}}, 200)
	closed := client.TimeEntry.GetX(ctx, entry.ID)
	if closed.ClockOut == nil || closed.JobID == nil || *closed.JobID != j.ID || closed.UserID != u.ID || !closed.ClockIn.Equal(entry.ClockIn) {
		t.Fatal("own close-out changed the retained entry's anchors")
	}
	snapshot := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	rows := snapshot["records"].([]any)
	if len(rows) != 1 {
		t.Fatalf("own clock snapshot=%v", snapshot)
	}
	row := rows[0].(map[string]any)
	job := row["job"].(map[string]any)
	if len(job) != 2 || job["id"] != fmt.Sprint(j.ID) || job["title"] != "Retained title" || strings.Contains(fmt.Sprint(snapshot), "PRIVATE") {
		t.Fatalf("unsafe clock context=%v", snapshot)
	}
	if _, found := row["gps"]; found {
		t.Fatal("Capture authority disclosed GPS without View")
	}
	var observations int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM time_clock_gps WHERE entry_id=$1`, entry.ID).Scan(&observations))
	if observations != 1 {
		t.Fatalf("retained GPS observations=%d", observations)
	}
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "job_id": j.ID}, 404)
	fieldPost(t, web, adminCookie, path+"/restore", nil, 303)
	// A terminally denied pending intent is not revived by Restore. Only an
	// explicit new operation reviewed against current Job state may proceed.
	executionAPI(t, api, token, "POST", path+"/field-notes", pending, 404)
	executionAPI(t, api, token, "POST", path+"/field-notes", executionNotes(t, api, token, path, "Fresh reviewed work"), 200)
}

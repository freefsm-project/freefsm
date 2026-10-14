package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/google/uuid"
)

// Exercise the atomic clock writer through HTTP, then read its Job history
// through the public API and the authorized durable CSV artifact.
func TestClockJobActivityOccurrenceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Clock history customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Clock history job").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Clock activity device")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	call := func(method, path string, command any) string {
		t.Helper()
		code, body := clockReviewCall(t, server, token, method, path, command)
		if code != 200 {
			t.Fatalf("%s %s = %d: %s", method, path, code, body)
		}
		return body
	}
	var lease struct {
		Data authorization.ClockLease `json:"data"`
	}
	if err := json.Unmarshal([]byte(call("POST", "/time-entries/lease", map[string]any{})), &lease); err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{}
	cookie := sessionCookie(t, ctx, sessions, admin)
	var entryID int64
	for _, kind := range []string{"in", "out"} {
		time.Sleep(10 * time.Millisecond)
		occurrence := time.Now().UTC().Truncate(time.Microsecond)
		action := "clocked_" + kind
		want[action] = occurrence
		command := map[string]any{"operation_id": uuid.NewString(), "lease_id": lease.Data.ID, "occurred_at": occurrence}
		if kind == "out" {
			command["entry_id"] = entryID
		}
		time.Sleep(10 * time.Millisecond)
		path := fmt.Sprintf("/jobs/%d/clock-%s", job.ID, kind)
		if kind == "out" {
			path = "/time-entries/clock-out"
		}
		historyPath := fmt.Sprintf("/jobs/%d/activity", job.ID)
		beforeHistory := call("GET", historyPath, nil)
		beforeActive := call("GET", "/time-entries/active", nil)
		auditPath := "/settings/audit?permission=time.clock_" + kind + "&channel=api"
		assertContains(t, requestBody(t, router, cookie, "GET", auditPath, 200), "No matching audit evidence.")
		// Failure after both activity inserts must roll back history, the clock
		// effect, and admission so this exact operation can subsequently succeed.
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_clock_activity_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('time.clock_in','time.clock_out') THEN RAISE EXCEPTION 'injected clock audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_clock_activity_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_clock_activity_audit()`); err != nil {
			t.Fatal(err)
		}
		code, failed := clockReviewCall(t, server, token, "POST", path, command)
		if code != 503 {
			t.Fatalf("audit failure = %d: %s", code, failed)
		}
		if after := call("GET", historyPath, nil); after != beforeHistory {
			t.Fatalf("audit failure changed Job activity: %s", after)
		}
		if after := call("GET", "/time-entries/active", nil); after != beforeActive {
			t.Fatalf("audit failure changed active clock: %s", after)
		}
		assertContains(t, requestBody(t, router, cookie, "GET", auditPath, 200), "No matching audit evidence.")
		if _, err := pool.Exec(ctx, `DROP TRIGGER reject_clock_activity_audit ON authorization_audit; DROP FUNCTION reject_clock_activity_audit()`); err != nil {
			t.Fatal(err)
		}
		body := call("POST", path, command)
		var receipt struct {
			Data struct {
				EntryID int64 `json:"entry_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &receipt); err != nil {
			t.Fatal(err)
		}
		if kind == "in" {
			entryID = receipt.Data.EntryID
		}
		call("POST", path, command) // replay must not publish a second activity
		assertContains(t, requestBody(t, router, cookie, "GET", auditPath, 200), command["operation_id"].(string))
	}
	var history struct {
		Data struct {
			Records []authorization.ActivityRecord `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(call("GET", fmt.Sprintf("/jobs/%d/activity", job.ID), nil)), &history); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, event := range history.Data.Records {
		occurrence, ok := want[event.Action]
		if !ok {
			continue
		}
		seen[event.Action]++
		if event.Occurred == nil || !event.Occurred.Equal(occurrence) || !event.Accepted.After(occurrence) {
			t.Errorf("%s occurrence=%v acceptance=%v; want occurrence %v before acceptance", event.Action, event.Occurred, event.Accepted, occurrence)
		}
	}
	for action := range want {
		if seen[action] != 1 {
			t.Errorf("%s events=%d want 1", action, seen[action])
		}
	}
	path := masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}, "object_type": {"job"}, "object_id": {fmt.Sprint(job.ID)}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for action, occurrence := range want {
		count := 0
		for _, row := range rows[1:] {
			if row[8] != action {
				continue
			}
			count++
			accepted, err := time.Parse(time.RFC3339Nano, row[1])
			if err != nil || !accepted.After(occurrence) || row[2] != occurrence.Format(time.RFC3339Nano) {
				t.Errorf("%s CSV occurrence/acceptance mismatch: %v", action, row)
			}
		}
		if count != 1 {
			t.Errorf("%s CSV rows=%d want 1", action, count)
		}
	}
}

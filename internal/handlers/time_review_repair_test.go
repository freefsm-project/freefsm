package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
)

func TestTimeManualPreflightRejectedReceiptHTTP(t *testing.T) {
	for _, field := range []string{"clock_in", "clock_out", "user_id", "job_id", "gps"} {
		t.Run(field, func(t *testing.T) {
			pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			values := url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-01T10:00:00Z"}, "clock_out": {"2026-01-01T11:00:00Z"}, "notes": {"retained manual draft"}}
			values.Set(field, "invalid")
			values = timeFormValues(t, router, cookie, "/time-entries", values)
			response := timeHTTP(router, cookie, "/time-entries", values)
			if response.Code != 400 && response.Code != 409 {
				t.Fatalf("validation=%d %s", response.Code, response.Body)
			}
			var receipt saveaction.Receipt
			body := requestBody(t, router, cookie, "GET", "/save-actions/"+values.Get("save_action_id"), 200)
			if err := json.Unmarshal([]byte(body), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Disposition != "rejected" || !receipt.RetrySafe || receipt.RetrySameOperation || receipt.CommittedSteps != 0 {
				t.Fatalf("preflight receipt=%s", body)
			}
			assertContains(t, response.Body.String(), "retained manual draft")
			assertContains(t, response.Body.String(), `name="clock_in"`)
			if field == "clock_in" || field == "clock_out" {
				assertContains(t, response.Body.String(), `value="invalid"`)
			}
			var history int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_entry_history`).Scan(&history); err != nil {
				t.Fatal(err)
			}
			if client.TimeEntry.Query().CountX(ctx) != 0 || history != 0 {
				t.Fatal("preflight wrote time facts")
			}
			values.Set("user_id", fmt.Sprint(admin))
			values.Set("clock_in", "2026-01-01T10:00:00Z")
			values.Set("clock_out", "2026-01-01T11:00:00Z")
			values.Del("job_id")
			values.Del("gps")
			values.Set("save_action_id", uuid.NewString())
			itemPost(t, router, cookie, "/time-entries", values, 303)
			if client.TimeEntry.Query().CountX(ctx) != 1 {
				t.Fatal("corrected fresh identity did not create once")
			}
		})
	}
}

func TestTimeConfirmationFailureOriginalIdentityHTTP(t *testing.T) {
	for _, action := range []string{"create", "correct", "void"} {
		t.Run(action, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
			entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("before confirmation failure").SaveX(ctx)
			path := fmt.Sprintf("/time-entries/%d", entry.ID)
			values := url.Values{"reason": {"Reviewed receipt recovery"}, "notes": {"after confirmation recovery"}}
			if action == "void" {
				path += "/void"
			}
			if action == "create" {
				path = "/time-entries"
				values = url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-02T10:00:00Z"}, "clock_out": {"2026-01-02T11:00:00Z"}, "notes": {"after confirmation recovery"}}
			}
			values = timeFormValues(t, router, cookie, path, values)
			operation := values.Get("save_action_id")
			if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_time_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.domain_committed_at IS NOT NULL AND OLD.domain_committed_at IS NULL THEN RAISE EXCEPTION 'injected domain confirmation failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_time_confirmation BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION reject_time_confirmation()`); err != nil {
				t.Fatal(err)
			}
			itemPost(t, router, cookie, path, values, 503)
			current := client.TimeEntry.GetX(ctx, entry.ID)
			if current.Notes != entry.Notes || current.VoidedAt != nil || client.TimeEntry.Query().CountX(ctx) != 1 {
				t.Fatal("confirmation failure changed projection")
			}
			var effects int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM time_entry_history)+(SELECT count(*) FROM authorization_audit WHERE target_type='time_entry')+(SELECT count(*) FROM activity_logs WHERE object_type='time_entry')+(SELECT count(*) FROM online_save_effects WHERE operation_id=$1)`, operation).Scan(&effects); err != nil || effects != 0 {
				t.Fatalf("confirmation failure leaked history/audit/effects: %d %v", effects, err)
			}
			var receipt saveaction.Receipt
			body := requestBody(t, router, cookie, "GET", "/save-actions/"+operation, 200)
			if err := json.Unmarshal([]byte(body), &receipt); err != nil {
				t.Fatal(err)
			}
			if !receipt.RetrySameOperation || receipt.CommittedSteps != 0 || receipt.Disposition != "unknown" {
				t.Fatalf("confirmation rollback not retryable: %s", body)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_time_confirmation ON online_save_actions`); err != nil {
				t.Fatal(err)
			}
			itemPost(t, router, cookie, path, values, 303)
			itemPost(t, router, cookie, path, values, 409)
			body = requestBody(t, router, cookie, "GET", "/save-actions/"+operation, 200)
			receipt = saveaction.Receipt{}
			if err := json.Unmarshal([]byte(body), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Disposition != "saved" || receipt.RetrySameOperation || receipt.CommittedSteps != 1 {
				t.Fatalf("recovery not confirmed once: %s", body)
			}
			var histories int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_entry_history`).Scan(&histories); err != nil || histories != 1 {
				t.Fatalf("recovery history=%d err=%v", histories, err)
			}
		})
	}
}

// This error is raised during Commit, after ConfirmDomain has succeeded. The
// generic boundary must not infer rollback from a Commit error or permit replay.
func TestTimeCommitStageFailureRemainsQuarantinedHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("before commit-stage failure").SaveX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	values := timeFormValues(t, router, cookie, path, url.Values{"reason": {"commit-stage control"}, "notes": {"must not automatically retry"}})
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_time_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit-stage error'; END $$; CREATE CONSTRAINT TRIGGER reject_time_commit AFTER INSERT ON time_entry_history DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_time_commit()`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, values, 503)
	var receipt saveaction.Receipt
	body := requestBody(t, router, cookie, "GET", "/save-actions/"+values.Get("save_action_id"), 200)
	if err := json.Unmarshal([]byte(body), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != "review" || receipt.RetrySameOperation || receipt.RetrySafe {
		t.Fatalf("commit error incorrectly declared rolled back: %s", body)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_time_commit ON time_entry_history`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, values, 409)
	if client.TimeEntry.GetX(ctx, entry.ID).Notes != entry.Notes {
		t.Fatal("quarantined original identity replayed")
	}
}

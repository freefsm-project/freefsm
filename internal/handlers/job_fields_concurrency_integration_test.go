package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestJobFieldsCommitTimeAuthorityRacesHTTP(t *testing.T) {
	for _, loss := range []string{"grant", "assignment", "archive", "session"} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/batch=%t", loss, batch), func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race customer").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Race job").SaveX(ctx)
				u := client.User.Create().SetCompanyID(company).SetEmail("race-fields@test").SetName("Field worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
				if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
					t.Fatal(err)
				}
				client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
				cookie := sessionCookie(t, ctx, sessions, admin)
				for _, name := range []string{"First", "Second"} {
					fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {name}, "field_type": {"text"}}, 303)
				}
				token, err := sessions.CreateMobile(ctx, u.ID, "Race client")
				if err != nil {
					t.Fatal(err)
				}
				api := apiv1.NewRouter(pool, client, sessions)
				path := fmt.Sprintf("/jobs/%d/custom-fields", j.ID)
				call := func(method, body string) *httptest.ResponseRecorder {
					target := path
					if batch && method == "PATCH" {
						method, target = "POST", "/sync/operations"
					}
					r := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctx)
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					return w
				}
				var state struct {
					Data authorization.FieldState `json:"data"`
				}
				read := call("GET", "")
				if read.Code != 200 {
					t.Fatal(read.Body.String())
				}
				if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil {
					t.Fatal(err)
				}
				command, _ := json.Marshal(authorization.ReviewedFieldPatch{OperationID: newOperationKey(), Revisions: state.Data.Revisions, Values: json.RawMessage(`[{"definition_id":1,"value":"must not persist"},{"definition_id":2,"value":"nor this"}]`)})
				if batch {
					var op map[string]any
					if err := json.Unmarshal(command, &op); err != nil {
						t.Fatal(err)
					}
					op["type"], op["target"], op["depends_on"] = "job.fields", fmt.Sprint(j.ID), []string{}
					command, _ = json.Marshal(map[string]any{"operations": []any{op}})
				}
				blocker, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(ctx)
				if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
					t.Fatal(err)
				}
				var pid int
				if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				assertForeignAuthorityExcluded(t, ctx, pool, int32(pid))
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- call("PATCH", string(command)) }()
				waitAuthorityRequest(t, ctx, pool, int32(pid), authorityWriterStatement)
				var statement string
				switch loss {
				case "grant":
					statement = `DELETE FROM authorization_grants WHERE role_id=3 AND permission='job.fields.edit'`
				case "assignment":
					statement = fmt.Sprintf(`DELETE FROM job_assignments WHERE job_id=%d AND user_id=%d`, j.ID, u.ID)
				case "archive":
					statement = fmt.Sprintf(`UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=%d`, j.ID)
				case "session":
					statement = fmt.Sprintf(`UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=%d`, u.ID)
				}
				if _, err := blocker.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				var response *httptest.ResponseRecorder
				select {
				case response = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("field request did not finish after fence release")
				}
				if batch {
					if response.Code != 200 || (!strings.Contains(response.Body.String(), `"code":"forbidden"`) && !strings.Contains(response.Body.String(), `"code":"not_found"`)) {
						t.Fatalf("batch loss accepted: %d %s", response.Code, response.Body.String())
					}
				} else if response.Code != 401 && response.Code != 403 && response.Code != 404 {
					t.Fatalf("loss accepted: %d %s", response.Code, response.Body.String())
				}
				assertNotContains(t, response.Body.String(), "comparison")
				var values string
				if err := pool.QueryRow(ctx, `SELECT custom_fields::text FROM jobs WHERE id=$1`, j.ID).Scan(&values); err != nil {
					t.Fatal(err)
				}
				if values != "[]" {
					t.Fatalf("partial effects after loss: %s", values)
				}
				var auditCount int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND action='job.fields.edit'`, j.ID).Scan(&auditCount); err != nil {
					t.Fatal(err)
				}
				if auditCount != 0 {
					t.Fatal("denial fabricated successful audit")
				}
			})
		}
	}
}

func TestJobFieldsConcurrentSameAndDisjointPatchesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Concurrent job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, name := range []string{"First", "Second"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {name}, "field_type": {"text"}}, 303)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Concurrent client")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/custom-fields", j.ID)
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, same := range []bool{false, true} {
		var state struct {
			Data authorization.FieldState `json:"data"`
		}
		if err := json.Unmarshal(call("GET", "").Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan *httptest.ResponseRecorder, 2)
		for index := 1; index <= 2; index++ {
			field := index
			if same {
				field = 1
			}
			body, _ := json.Marshal(authorization.ReviewedFieldPatch{OperationID: newOperationKey(), Revisions: state.Data.Revisions, Values: json.RawMessage(fmt.Sprintf(`[{"definition_id":%d,"value":%q}]`, field, fmt.Sprintf("round-%t-writer-%d", same, index)))})
			go func(body string) { <-start; done <- call("PATCH", body) }(string(body))
		}
		close(start)
		accepted, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			response := <-done
			switch response.Code {
			case 200:
				accepted++
			case 409:
				conflicts++
			default:
				t.Fatalf("unexpected %d: %s", response.Code, response.Body.String())
			}
		}
		if !same && (accepted != 2 || conflicts != 0) || same && (accepted != 1 || conflicts != 1) {
			t.Fatalf("same=%t accepted=%d conflicts=%d", same, accepted, conflicts)
		}
	}
	assertContains(t, call("GET", "").Body.String(), "round-false-writer-2")
}

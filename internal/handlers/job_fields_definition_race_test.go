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
)

func TestJobFieldsDefinitionMutationWinsBatchHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Definition race").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Definition race").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Definition contender")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	initial := call("GET", "/sync/jobs", "")
	if initial.Code != 200 {
		t.Fatal(initial.Body.String())
	}
	var snapshot struct {
		Data struct {
			CompletionCursor string `json:"completion_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(initial.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	form := currentJobFormValues(t, web, cookie, "/settings/custom-fields/1", url.Values{"name": {"Reading"}, "field_type": {"text"}, "retired": {"true"}})
	// Pause the actual retirement UPDATE while its writer holds authority190019.
	// The fixture/row/statement witness excludes pre-handler scheduling as proof.
	_, err = pool.Exec(ctx, `CREATE FUNCTION pause_field_retirement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=1 THEN PERFORM pg_advisory_xact_lock(820082); END IF; RETURN NEW; END $$; CREATE TRIGGER field_retirement_barrier BEFORE UPDATE OF retired ON custom_field_definitions FOR EACH ROW EXECUTE FUNCTION pause_field_retirement()`)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(820082)`); err != nil {
		t.Fatal(err)
	}
	var holder int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	definitionDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/settings/custom-fields/1", strings.NewReader(form.Encode())).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		definitionDone <- w
	}()
	var writer int32
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		err := pool.QueryRow(ctx, `SELECT coalesce((SELECT a.pid FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.wait_event='advisory' AND a.query LIKE 'UPDATE custom_field_definitions SET retired=%' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND l.objsubid=1 AND l.granted)),0)`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer)
		if err != nil {
			t.Fatal(err)
		}
		if writer != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if writer == 0 {
		t.Fatal("retirement writer never reached actual definition UPDATE")
	}
	t.Logf("definition mutation backend=%d held authority190019 and blocked by=%d at UPDATE retired", writer, holder)
	assertForeignAuthorityExcluded(t, ctx, pool, writer)
	batchDone := make(chan *httptest.ResponseRecorder, 1)
	body := fmt.Sprintf(`{"operations":[{"operation_id":"82000000-0000-4000-8000-000000000031","type":"job.fields","target":"%d","depends_on":[],"revisions":{"1":"00000000-0000-0000-0000-000000000000"},"values":[{"definition_id":1,"value":"must not persist after retirement"}]}]}`, j.ID)
	go func() { batchDone <- call("POST", "/sync/operations", body) }()
	waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-definitionDone:
		if w.Code != 303 {
			t.Fatalf("definition writer: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("definition writer did not finish")
	}
	select {
	case w := <-batchDone:
		if w.Code != 200 {
			t.Fatalf("batch: %d %s", w.Code, w.Body.String())
		}
		assertContains(t, w.Body.String(), `"reason":"retired_field"`)
	case <-time.After(5 * time.Second):
		t.Fatal("field batch did not finish")
	}
	state := call("GET", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "")
	if state.Code != 200 {
		t.Fatal(state.Body.String())
	}
	assertNotContains(t, state.Body.String(), "must not persist")
	delta := call("GET", "/sync/jobs?cursor="+url.QueryEscape(snapshot.Data.CompletionCursor), "")
	if delta.Code != 200 {
		t.Fatal(delta.Body.String())
	}
	var result struct {
		Data struct {
			Records []struct {
				Fields struct {
					Definitions []struct {
						Retired bool `json:"retired"`
					} `json:"definitions"`
					History   []any             `json:"history"`
					Revisions map[string]string `json:"revisions"`
				} `json:"custom_fields"`
			} `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(delta.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Records) != 1 || !result.Data.Records[0].Fields.Definitions[0].Retired || len(result.Data.Records[0].Fields.History) != 0 || result.Data.Records[0].Fields.Revisions["1"] != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("definition-only delta invented a value/history change: %s", delta.Body.String())
	}
}

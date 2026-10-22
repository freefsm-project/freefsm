package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func commentSyncBody(job int64, kind string, intent map[string]any, revision string) (string, string) {
	id := uuid.NewString()
	intent["parent_type"] = "job"
	intent["parent_revision"] = revision
	body, _ := json.Marshal(map[string]any{"operations": []any{map[string]any{"operation_id": id, "type": kind, "target": fmt.Sprint(job), "depends_on": []string{}, "comment": intent}}})
	return string(body), id
}

func TestCommentSyncActualMutationClosureRaceHTTP(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(replyFirst), func(t *testing.T) {
			f, api, _, _, _, jobs := syncFixture(t, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			token, err := f.sessions.CreateMobile(ctx, f.admin, "Comment DML race")
			if err != nil {
				t.Fatal(err)
			}
			service := authorization.New(f.pool)
			parent := objectref.New(objectref.TypeJob, jobs[0])
			actor := authorization.Actor{UserID: f.admin}
			root, err := service.AddComment(ctx, actor, parent, "root", 0)
			if err != nil {
				t.Fatal(err)
			}
			revision := commentRevisionHTTP(t, api, token, "job", jobs[0])
			condition := "TG_OP='UPDATE' AND NEW.removed_at IS DISTINCT FROM OLD.removed_at"
			statement := "UPDATE comments%"
			if replyFirst {
				condition = "TG_OP='INSERT' AND NEW.reply_to IS NOT NULL"
				statement = "INSERT INTO comments%"
			}
			if _, err := f.pool.Exec(ctx, `CREATE FUNCTION hold_comment_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN PERFORM pg_advisory_xact_lock(840084); END IF; RETURN NEW; END $$; CREATE TRIGGER hold_comment_mutation BEFORE INSERT OR UPDATE ON comments FOR EACH ROW EXECUTE FUNCTION hold_comment_mutation()`); err != nil {
				t.Fatal(err)
			}
			barrier, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			var blocker int32
			if err := barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(840084)) barrier`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			reply, _ := commentSyncBody(jobs[0], "comment.add", map[string]any{"content": "racing reply", "reply_to": fmt.Sprint(root)}, revision)
			remove, _ := commentSyncBody(jobs[0], "comment.remove", map[string]any{"remove_id": fmt.Sprint(root)}, revision)
			first, second := remove, reply
			if replyFirst {
				first, second = reply, remove
			}
			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- batchRequest(api, token, first) }()
			var writer int32
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				err := f.pool.QueryRow(ctx, `SELECT a.pid FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.wait_event='advisory' AND a.query LIKE $3 AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND l.granted)`, blocker, f.pool.Config().ConnConfig.RuntimeParams["application_name"], statement).Scan(&writer)
				if err == nil {
					break
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if writer == 0 {
				t.Fatal("actual Comment DML did not reach fixture barrier while holding authority")
			}
			t.Logf("actual Comment DML fixture=%s writer=%d blocker=%d statement=%s", f.pool.Config().ConnConfig.RuntimeParams["application_name"], writer, blocker, statement)
			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { secondDone <- batchRequest(api, token, second) }()
			waitAuthorityRequest(t, ctx, f.pool, writer, authorityWriterStatement)
			if err := barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			one, two := <-firstDone, <-secondDone
			want := "comment_conflict"
			if replyFirst {
				want = "accepted"
			}
			if one.Code != 200 || !strings.Contains(one.Body.String(), `"code":"accepted"`) || two.Code != 200 || !strings.Contains(two.Body.String(), `"code":"`+want+`"`) {
				t.Fatalf("DML ordering first=%d %s second=%d %s", one.Code, one.Body.String(), two.Code, two.Body.String())
			}
			before, err := service.Comments(ctx, actor, parent)
			if err != nil {
				t.Fatal(err)
			}
			if before.Entries[0].Content != "" || before.Entries[0].RemovedAt == nil || before.Entries[0].CanReply {
				t.Fatalf("root placeholder: %+v", before)
			}
			wantCount := 1
			if replyFirst {
				wantCount = 2
			}
			if len(before.Entries) != wantCount {
				t.Fatalf("reply ordering: %+v", before)
			}
			noop, _ := commentSyncBody(jobs[0], "comment.remove", map[string]any{"remove_id": fmt.Sprint(root)}, revision)
			if w := batchRequest(api, token, noop); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"accepted"`) {
				t.Fatalf("no-op: %d %s", w.Code, w.Body.String())
			}
			after, err := service.Comments(ctx, actor, parent)
			if err != nil {
				t.Fatal(err)
			}
			if !before.Entries[0].RemovedAt.Equal(*after.Entries[0].RemovedAt) || *before.Entries[0].RemovedBy != *after.Entries[0].RemovedBy {
				t.Fatal("no-op changed original removal attribution")
			}
			var audits int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='comment.remove'`).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("removal history count=%d err=%v", audits, err)
			}
		})
	}
}

func TestCommentSyncAuditRollbackAndSameIDRetryHTTP(t *testing.T) {
	f, api, _, _, _, jobs := syncFixture(t, 1)
	ctx := context.Background()
	token, err := f.sessions.CreateMobile(ctx, f.admin, "Comment fault")
	if err != nil {
		t.Fatal(err)
	}
	body, id := commentSyncBody(jobs[0], "comment.add", map[string]any{"content": "atomic original"}, commentRevisionHTTP(t, api, token, "job", jobs[0]))
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_sync_comment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='comment.add' THEN RAISE EXCEPTION 'injected Comment audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_sync_comment_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_sync_comment_audit()`); err != nil {
		t.Fatal(err)
	}
	w := batchRequest(api, token, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"retryable_failure"`) || strings.Contains(w.Body.String(), `"receipt"`) {
		t.Fatalf("fault: %d %s", w.Code, w.Body.String())
	}
	var effects int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM comments)+(SELECT count(*) FROM comment_operation_receipts WHERE operation_id=$1)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, id).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("partial effect/admission/receipt=%d %v", effects, err)
	}
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER reject_sync_comment_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if w := batchRequest(api, token, body); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"accepted"`) {
			t.Fatalf("same-ID retry: %d %s", w.Code, w.Body.String())
		}
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("duplicated effect=%d %v", effects, err)
	}
}

func TestCommentSyncDirectTransportAndArchivedVisibilityHTTP(t *testing.T) {
	f, _, _, _, _, jobs := syncFixture(t, 1)
	ctx := context.Background()
	token, err := f.sessions.CreateMobile(ctx, f.admin, "Comment transport")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	revision := commentRevisionHTTP(t, api, token, "job", jobs[0])
	for _, body := range []string{
		fmt.Sprintf(`{"operation_id":%q,"content":"first","content":"second"}`, uuid.NewString()),
		fmt.Sprintf(`{"operation_id":%q,"content":"text","reply_to":null}`, uuid.NewString()),
	} {
		r := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/comments", jobs[0]), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("malformed transport admitted: %d %s", w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/jobs/%d/comments?expand=audit", jobs[0]), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Errorf("unsupported expansion: %d", w.Code)
	}
	var effects int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM comments)+(SELECT count(*) FROM comment_operation_receipts)+(SELECT count(*) FROM operation_admissions WHERE operation_type='comment')`).Scan(&effects); err != nil || effects != 0 {
		t.Errorf("malformed transport had effects=%d err=%v", effects, err)
	}
	missing := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/comments", jobs[0]), strings.NewReader(fmt.Sprintf(`{"operation_id":%q,"content":"unguarded new direct intent"}`, uuid.NewString())))
	missing.Header.Set("Authorization", "Bearer "+token)
	missing.Header.Set("Content-Type", "application/json")
	unbound := httptest.NewRecorder()
	api.ServeHTTP(unbound, missing)
	if unbound.Code != 409 || !strings.Contains(unbound.Body.String(), "comment_parent_changed") {
		t.Fatalf("unguarded direct API effect: %d %s", unbound.Code, unbound.Body.String())
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("unguarded direct Comment created: %d %v", effects, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, jobs[0]); err != nil {
		t.Fatal(err)
	}
	body, _ := commentSyncBody(jobs[0], "comment.add", map[string]any{"content": "archived Job"}, revision)
	w = batchRequest(api, token, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Errorf("archived field parent visible: %d %s", w.Code, w.Body.String())
	}
}

func TestCommentSyncOwnRemovalRechecksAssignmentAndRejectsBroadOfficeRemovalHTTP(t *testing.T) {
	f, api, token, uid, _, jobs := syncFixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	service := authorization.New(f.pool)
	role, err := service.SaveRole(ctx, authorization.Actor{UserID: f.admin}, 0, "Own Comment author", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "comment.add", Scope: authorization.Assigned}, {Permission: "comment.remove", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AssignUser(ctx, authorization.Actor{UserID: f.admin}, uid, role); err != nil {
		t.Fatal(err)
	}
	parent := objectref.New(objectref.TypeJob, jobs[0])
	root, err := service.AddComment(ctx, authorization.Actor{UserID: uid}, parent, "original own content", 0)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, err := f.sessions.CreateMobile(ctx, f.admin, "Broad office removal denied")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := commentSyncBody(jobs[0], "comment.remove", map[string]any{"remove_id": fmt.Sprint(root)}, commentRevisionHTTP(t, api, adminToken, "job", jobs[0]))
	if w := batchRequest(api, adminToken, body); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("broader office grant removed another author: %d %s", w.Code, w.Body.String())
	}
	revision := commentRevisionHTTP(t, api, token, "job", jobs[0])
	fence, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(context.Background())
	var blocker int32
	if err := fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) authority`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	assertForeignAuthorityExcluded(t, ctx, f.pool, blocker)
	body, _ = commentSyncBody(jobs[0], "comment.remove", map[string]any{"remove_id": fmt.Sprint(root)}, revision)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- batchRequest(api, token, body) }()
	waitAuthorityRequest(t, ctx, f.pool, blocker, authorityWriterStatement)
	if _, err := fence.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, jobs[0], uid); err != nil {
		t.Fatal(err)
	}
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Fatalf("Own bypassed current assignment: %d %s", w.Code, w.Body.String())
	}
	comments, err := service.Comments(ctx, authorization.Actor{UserID: f.admin}, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments.Entries) != 1 || comments.Entries[0].RemovedAt != nil || comments.Entries[0].Content != "original own content" {
		t.Fatalf("denied removal changed original: %+v", comments)
	}
}

func TestCommentSyncRemovedTextWithheldOnMaterializedReplayHTTP(t *testing.T) {
	f, api, token, _, _, jobs := syncFixture(t, 1)
	ctx := context.Background()
	service := authorization.New(f.pool)
	actor := authorization.Actor{UserID: f.admin}
	parent := objectref.New(objectref.TypeJob, jobs[0])
	id, err := service.AddComment(ctx, actor, parent, "later removed text", 0)
	if err != nil {
		t.Fatal(err)
	}
	first := syncGet(t, api, token, "", 200)
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET job_type='Unrelated edit' WHERE id=$1`, jobs[0]); err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, token, "?cursor="+*first.Cursor, 200)
	if err := service.RemoveComment(ctx, actor, parent, id); err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, token, "?cursor="+*first.Cursor, 409)
	fresh := syncGet(t, api, token, "", 200)
	if strings.Contains(fmt.Sprint(fresh), "later removed text") {
		t.Fatal("removed text disclosed by fresh snapshot")
	}
}

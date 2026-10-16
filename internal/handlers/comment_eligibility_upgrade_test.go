package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/google/uuid"
)

// A real pre-eligibility source tree, including its original receipt writers.
const commentEligibilityInstalledSource = "24146beb22308c7f2a5107e53f708ded58b99842"

func TestCommentSyncEligibilityInstalledUpgradeHTTP(t *testing.T) {
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = "../.."
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return out
	}
	old := fstest.MapFS{}
	const prefix = "internal/database/migrations/"
	for _, name := range strings.Fields(string(git("ls-tree", "-r", "--name-only", commentEligibilityInstalledSource, "--", prefix))) {
		if strings.HasSuffix(name, ".up.sql") {
			old[strings.TrimPrefix(name, prefix)] = &fstest.MapFile{Data: git("show", commentEligibilityInstalledSource+":"+name)}
		}
	}
	t.Logf("pre087 source=%s migration-tree=%s", commentEligibilityInstalledSource, strings.TrimSpace(string(git("rev-parse", commentEligibilityInstalledSource+":internal/database/migrations"))))
	pool, client, _, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed owner").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Installed parent").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Eligibility upgrade")
	if err != nil {
		t.Fatal(err)
	}
	var clientHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND device_name='Eligibility upgrade'`, admin).Scan(&clientHash); err != nil {
		t.Fatal(err)
	}
	jobAlias := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO job_sync_identities(resource_type,installed_id,opaque_id) VALUES('job',$1,$2)`, job.ID, jobAlias); err != nil {
		t.Fatal(err)
	}
	type installed struct {
		op, body string
		batch    bool
		comment  int64
		receipt  string
	}
	var entries []installed
	for _, batch := range []bool{false, true} {
		op := uuid.NewString()
		text := "installed direct"
		if batch {
			text = "installed batch"
		}
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($1,'job',$2,$3,$4) RETURNING id`, company, job.ID, admin, text).Scan(&id); err != nil {
			t.Fatal(err)
		}
		// Freeze the actual old serialization, independently of today's structs.
		encoded := fmt.Sprintf(`{"Type":"job","ID":%d,"Command":{"operation_id":%q,"content":%q}}:remove:0`, job.ID, op, text)
		body := fmt.Sprintf(`{"operation_id":%q,"content":%q}`, op, text)
		if batch {
			encoded = fmt.Sprintf(`{"Batch":{"operation_id":%q,"type":"comment.add","target":%q,"depends_on":[],"notes_revision":"","field_notes":null,"comment":{"parent_type":"job","content":%q}}}`, op, fmt.Sprint(job.ID), text)
			body = fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":"comment.add","target":%q,"depends_on":[],"comment":{"parent_type":"job","content":%q}}]}`, op, fmt.Sprint(job.ID), text)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))
		if _, err := pool.Exec(ctx, `INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type,intent) SELECT $1,id,$2,$3,'comment',to_jsonb($4::text) FROM operation_deployment`, op, admin, "api:"+clientHash, digest); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO comment_operation_receipts(user_id,operation_id,client_key,intent_hash,comment_id,code) VALUES($1,$2,$3,$4,$5,'accepted')`, admin, op, "api:"+clientHash, digest, id); err != nil {
			t.Fatal(err)
		}
		var receipt string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(r)::text FROM comment_operation_receipts r WHERE operation_id=$1`, op).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, installed{op, body, batch, id, receipt})
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
		 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),
		 'comments',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM comments c),
		 'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY operation_id) FROM comment_operation_receipts r),
		 'admissions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY operation_id) FROM operation_admissions a),
		 'aliases',(SELECT jsonb_agg(to_jsonb(i) ORDER BY resource_type,installed_id) FROM job_sync_identities i),
		 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g),
		 'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM authorization_audit a))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	db := &database.DB{Pool: pool}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); got != before {
		t.Fatal("eligibility upgrade rewrote installed facts, identifiers, grants or bindings")
	}
	var baseline string
	if err := pool.QueryRow(ctx, `SELECT revision::text FROM comment_parent_eligibility WHERE object_type='job' AND object_id=$1`, job.ID).Scan(&baseline); err != nil || baseline == "" {
		t.Fatalf("current baseline %q %v", baseline, err)
	}
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var repeated string
	if err := pool.QueryRow(ctx, `SELECT revision::text FROM comment_parent_eligibility WHERE object_type='job' AND object_id=$1`, job.ID).Scan(&repeated); err != nil || repeated != baseline {
		t.Fatalf("repeat migration changed current baseline: %v", err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	page := syncGet(t, api, token, "?scope=all", 200)
	if len(page.Records) != 1 || page.Records[0]["id"] != jobAlias {
		t.Fatalf("installed alias changed: %+v", page)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		path := fmt.Sprintf("/jobs/%d/comments", job.ID)
		if e.batch {
			path = "/sync/operations"
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(e.body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"accepted"`) {
			t.Fatalf("installed unguarded terminal replay batch=%v: %d %s", e.batch, w.Code, w.Body.String())
		}
		var after string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(r)::text FROM comment_operation_receipts r WHERE operation_id=$1`, e.op).Scan(&after); err != nil || after != e.receipt {
			t.Fatalf("installed receipt changed: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		public := decoded["data"].(map[string]any)
		if e.batch {
			public = public["results"].([]any)[0].(map[string]any)["receipt"].(map[string]any)
		}
		if public["comment_id"] != float64(e.comment) || strings.Contains(w.Body.String(), "installed ") {
			t.Fatalf("unsafe or changed installed effect identity: %s", w.Body.String())
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("installed replay duplicated effects: %d %v", count, err)
	}
}

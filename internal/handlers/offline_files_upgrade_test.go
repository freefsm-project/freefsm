package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

// Seed actual pre-registry tables, then run the installed upgrade. NULL registry
// intent is historical absence, never proof of a newly submitted batch intent.
func TestOfflineFilesInstalledReceiptsCannotAuthorizeBatchHTTP(t *testing.T) {
	source := database.MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() >= "082_" {
			continue
		}
		data, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	pool, client, router, sessions, company, user := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed File owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Original File parent").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(user).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, user, "Installed File client")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'job',$2,'original.txt','original.txt','text/plain',8,'retained-original',$3) RETURNING id`, company, j.ID, user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	publication, mutation := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO file_publications(user_id,operation_id,intent,file_id,client_key) SELECT $1,$2,'["original nonbatch intent"]',$3,'api:'||id::text FROM sessions WHERE user_id=$1 AND kind='mobile'`, user, publication, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_mutation_operations(user_id,operation_id,intent_hash,file_id,client_key) SELECT $1,$2,'original nonbatch hash',$3,token_hash FROM sessions WHERE user_id=$1 AND kind='mobile'`, user, mutation, id); err != nil {
		t.Fatal(err)
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &offlineFileFixture{associationRepairFixture: &associationRepairFixture{pool: pool, client: client, router: router, sessions: sessions, company: company, admin: user}, api: apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), root, 1024)), token: token, user: user, job: j.ID, uploadDir: root}
	for _, kind := range []string{"file.publish", "file.rename", "file.remove"} {
		t.Run(kind, func(t *testing.T) {
			opID, target := mutation, fmt.Sprint(id)
			if kind == "file.publish" {
				opID, target = publication, uuid.NewString()
			}
			original := f.call(t, "GET", "/file-operations/"+opID, nil, 200)
			if original["code"] != "accepted" || original["effect_id"] != fmt.Sprint(id) {
				t.Fatalf("installed receipt unavailable: %v", original)
			}
			before := f.call(t, "GET", fmt.Sprintf("/files/%d/metadata", id), nil, 200)
			op := fileBatchOp(opID, kind, target, j.ID)
			if kind != "file.publish" {
				op["revision"] = "original reviewed revision"
			}
			if kind == "file.rename" {
				op["name"] = "changed.txt"
			}
			notes := f.call(t, "GET", fmt.Sprintf("/jobs/%d/field-notes", j.ID), nil, 200)
			child := map[string]any{"operation_id": uuid.NewString(), "type": "job.field_notes", "target": fmt.Sprint(j.ID), "depends_on": []string{opID}, "notes_revision": notes["notes_revision"], "field_notes": "must not be authorized by borrowed success"}
			result := f.batch(t, op, child)
			first := result[0].(map[string]any)
			if first["receipt"] != nil || first["error"] == nil || first["error"].(map[string]any)["code"] != "operation_intent_changed" || first["error"].(map[string]any)["status"] != float64(409) {
				t.Fatalf("borrowed installed receipt: %v", first)
			}
			if result[1].(map[string]any)["receipt"].(map[string]any)["code"] != "failed_dependency" {
				t.Fatalf("borrowed predecessor success: %v", result)
			}
			if got := f.call(t, "GET", "/file-operations/"+opID, nil, 200); !reflect.DeepEqual(got, original) {
				t.Fatalf("original receipt lost: %v", got)
			}
			if got := f.call(t, "GET", fmt.Sprintf("/files/%d/metadata", id), nil, 200); !reflect.DeepEqual(got, before) {
				t.Fatalf("rejected batch changed File: before=%v after=%v", before, got)
			}
			if got := f.call(t, "GET", fmt.Sprintf("/jobs/%d/field-notes", j.ID), nil, 200); !reflect.DeepEqual(got, notes) {
				t.Fatalf("blocked descendant changed Notes: before=%v after=%v", notes, got)
			}
		})
	}
}

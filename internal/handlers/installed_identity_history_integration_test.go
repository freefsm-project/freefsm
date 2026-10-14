package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

// Last installed composition before the common registry existed. SQL comes
// from this object, not today's migration tree with an arbitrary file omitted.
const installedIdentitySource = "79b92c67b9a719e1ba733b50086eb8dd60cb578b"

func installedIdentityMigrations(t *testing.T) fstest.MapFS {
	t.Helper()
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = "../.."
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
		return b
	}
	const prefix = "internal/database/migrations/"
	migrations := fstest.MapFS{}
	for _, path := range strings.Fields(string(git("ls-tree", "-r", "--name-only", installedIdentitySource, "--", prefix))) {
		if strings.HasSuffix(path, ".up.sql") {
			migrations[strings.TrimPrefix(path, prefix)] = &fstest.MapFile{Data: git("show", installedIdentitySource+":"+path)}
		}
	}
	t.Logf("installed source=%s migration tree=%s", installedIdentitySource, strings.TrimSpace(string(git("rev-parse", installedIdentitySource+":internal/database/migrations"))))
	// Receipt shapes and client encodings are owned by these original writers.
	for _, path := range []string{"internal/authorization/comment_operations.go", "internal/authorization/field_reconciliation.go", "internal/authorization/commercial_operations.go", "internal/services/file.go", "internal/services/file_publication.go", "internal/services/file_mutation.go"} {
		b := git("show", installedIdentitySource+":"+path)
		t.Logf("receipt source %s sha256=%x", path, sha256.Sum256(b))
	}
	return migrations
}

func TestInstalledIdentityHistoryHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixtureMigrations(t, installedIdentityMigrations(t))
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Secret installed owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Secret installed target").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Installed client")
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	var session int64
	if err := pool.QueryRow(ctx, `SELECT id,token_hash FROM sessions WHERE user_id=$1 AND kind='mobile'`, admin).Scan(&session, &hash); err != nil {
		t.Fatal(err)
	}
	execSQL := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var role int64
	if err := pool.QueryRow(ctx, `INSERT INTO authorization_roles(name) VALUES('No installed target access') RETURNING id`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	execSQL(`INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) SELECT company_id,'retained-admin@test','Retained Administrator',password_hash,'admin',1 FROM users WHERE id=$1`, admin)
	var registry bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('operation_admissions') IS NOT NULL`).Scan(&registry); err != nil || registry {
		t.Fatalf("not an installed pre-registry schema: %v %v", registry, err)
	}
	var file, comment int64
	fileRoot := t.TempDir()
	newFile := func() int64 {
		t.Helper()
		filePath := filepath.Join(fileRoot, uuid.NewString()+".txt")
		if err := os.WriteFile(filePath, []byte("hello world"), 0600); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by,sha256) VALUES($1,'job',$2,'Secret installed file.txt','installed','text/plain',11,$4,$3,'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9') RETURNING id`, company, j.ID, admin, filePath).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	file = newFile()
	if err := pool.QueryRow(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($1,'job',$2,$3,'Secret installed comment') RETURNING id`, company, j.ID, admin).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	type reservation struct {
		name, id, kind, key, lookup string
		status                      int
	}
	var cases []reservation
	add := func(name, kind, key, lookup string, status int) string {
		id := uuid.NewString()
		cases = append(cases, reservation{name, id, kind, key, lookup, status})
		return id
	}
	commentID := add("comment", "comment", "api:"+hash, "/comment-operations/", 200)
	command := authorization.CommentCommand{OperationID: commentID, Content: "Secret installed comment"}
	// Freeze the installed writer's serialization, independently of today's
	// command structs. A future wire change must not silently remake old intent.
	encoded := []byte(fmt.Sprintf(`{"Type":"job","ID":%d,"Command":{"operation_id":%q,"content":"Secret installed comment"}}`, j.ID, commentID))
	digest := fmt.Sprintf("%x", sha256.Sum256(append(encoded, []byte(":remove:0")...)))
	execSQL(`INSERT INTO comment_operation_receipts(user_id,operation_id,client_key,intent_hash,comment_id,code) VALUES($1,$2,$3,$4,$5,'accepted')`, admin, commentID, "api:"+hash, digest, comment)
	var definition int64
	if err := pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,'job','Installed confidential value','text') RETURNING id`, company).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	execSQL(`UPDATE jobs SET custom_fields=jsonb_build_array(jsonb_build_object('definition_id',$2::bigint,'value','Secret installed field value')) WHERE id=$1`, j.ID, definition)
	fieldID := add("field_mobile", "job.fields", "api:"+hash, fmt.Sprintf("/jobs/%d/custom-fields?operation_id=", j.ID), 200)
	fieldIntent := []byte(fmt.Sprintf(`{"operation_id":%q,"revisions":{},"values":[]}`, fieldID))
	execSQL(`INSERT INTO job_field_operations(user_id,client_hash,operation_id,job_id,intent,outcome) VALUES($1,$2,$3,$4,$5,'accepted')`, admin, hash, fieldID, j.ID, fieldIntent)
	unknownField := add("field_missing_session", "job.fields", "legacy.unknown", fmt.Sprintf("/jobs/%d/custom-fields?operation_id=", j.ID), 404)
	if _, err := sessions.CreateMobile(ctx, admin, "Removed installed client"); err != nil {
		t.Fatal(err)
	}
	var removedHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND device_name='Removed installed client'`, admin).Scan(&removedHash); err != nil {
		t.Fatal(err)
	}
	unknownFieldIntent := []byte(fmt.Sprintf(`{"operation_id":%q,"revisions":{},"values":[]}`, unknownField))
	execSQL(`INSERT INTO job_field_operations(user_id,client_hash,operation_id,job_id,intent,outcome) VALUES($1,$5,$2,$3,$4,'accepted')`, admin, unknownField, j.ID, unknownFieldIntent, removedHash)
	ambiguousField := add("field_ambiguous_clients", "legacy.identity_conflict", "api:"+hash, fmt.Sprintf("/jobs/%d/custom-fields?operation_id=", j.ID), 200)
	ambiguousIntent := []byte(fmt.Sprintf(`{"operation_id":%q,"revisions":{},"values":[]}`, ambiguousField))
	execSQL(`INSERT INTO job_field_operations(user_id,client_hash,operation_id,job_id,intent,outcome) VALUES($1,$2,$3,$4,$5,'accepted'),($1,$6,$3,$4,$5,'accepted')`, admin, hash, ambiguousField, j.ID, ambiguousIntent, removedHash)
	execSQL(`DELETE FROM sessions WHERE user_id=$1 AND token_hash=$2`, admin, removedHash)
	if _, err := sessions.Create(ctx, admin); err != nil {
		t.Fatal(err)
	}
	var webHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND kind='web'`, admin).Scan(&webHash); err != nil {
		t.Fatal(err)
	}
	webField := add("field_web", "job.fields", "web", fmt.Sprintf("/jobs/%d/custom-fields?operation_id=", j.ID), 404)
	webFieldIntent := []byte(fmt.Sprintf(`{"operation_id":%q,"revisions":{},"values":[]}`, webField))
	execSQL(`INSERT INTO job_field_operations(user_id,client_hash,operation_id,job_id,intent,outcome) VALUES($1,$2,$3,$4,$5,'accepted')`, admin, webHash, webField, j.ID, webFieldIntent)
	commercialReplays := map[string]string{}
	for _, kind := range []string{"estimate", "invoice"} {
		id := add(kind, "commercial."+kind, "api:"+hash, "/commercial-operations/", 200)
		// Terminal historical validation receipt: the owning writer persists the
		// exact submitted intent and no effect for this disposition.
		intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"title":""}`, id, j.ID)
		receipt := fmt.Sprintf(`{"operation_id":%q,"outcome":"invalid_document"}`, id)
		execSQL(`INSERT INTO commercial_operations(user_id,client_hash,operation_id,document_type,requested_id,intent,receipt) VALUES($1,$2,$3,$4,0,$5,$6)`, admin, hash, id, kind, intent, receipt)
		acceptedID := add(kind+"_accepted", "commercial."+kind, "api:"+hash, "/commercial-operations/", 200)
		clientID := uuid.NewString()
		title := "Secret installed " + kind
		var effect int64
		var number string
		if err := pool.QueryRow(ctx, `INSERT INTO `+kind+`s(company_id,customer_id,job_id,title) VALUES($1,$2,$3,$4) RETURNING id,number_label`, company, c.ID, j.ID, title).Scan(&effect, &number); err != nil {
			t.Fatal(err)
		}
		revision, err := saveaction.Revision(ctx, pool, admin, fmt.Sprintf("/%ss/%d", kind, effect))
		if err != nil {
			t.Fatal(err)
		}
		acceptedIntent := []byte(fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":%q}`, acceptedID, clientID, j.ID, title))
		acceptedReceipt := []byte(fmt.Sprintf(`{"operation_id":%q,"id":%d,"number":%q,"revision":%q,"outcome":"accepted"}`, acceptedID, effect, number, revision))
		execSQL(`INSERT INTO commercial_client_identities(client_id,document_type,document_id,user_id,client_hash) VALUES($1,$2,$3,$4,$5)`, clientID, kind, effect, admin, hash)
		execSQL(`INSERT INTO commercial_operations(user_id,client_hash,operation_id,document_type,requested_id,intent,receipt) VALUES($1,$2,$3,$4,0,$5,$6)`, admin, hash, acceptedID, kind, acceptedIntent, acceptedReceipt)
		commercialReplays[kind] = string(acceptedIntent)
	}
	publication := add("publication_mobile", "file.publication", "api:"+hash, "/file-operations/", 200)
	publicationIntent := []byte(fmt.Sprintf(`["job",%d,"Secret installed file.txt",11,"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"]`, j.ID))
	execSQL(`INSERT INTO file_publications(user_id,operation_id,intent,file_id,client_key) VALUES($1,$2,$5,$3,$4)`, admin, publication, file, fmt.Sprintf("api:%d", session), string(publicationIntent))
	unknownPub := add("publication_unattributed", "file.publication", "api:legacy.unknown", "/file-operations/", 404)
	execSQL(`INSERT INTO file_publications(user_id,operation_id,intent,file_id) VALUES($1,$2,$4,$3)`, admin, unknownPub, newFile(), string(publicationIntent))
	webPub := add("publication_web", "file.publication", "web", "/file-operations/", 404)
	execSQL(`INSERT INTO file_publications(user_id,operation_id,intent,file_id,client_key) VALUES($1,$2,$4,$3,'web')`, admin, webPub, newFile(), string(publicationIntent))
	mutation := add("file_mutation", "file.mutation", "api:"+hash, "/file-operations/", 200)
	mutationCommand := services.FileMutation{FileOperation: authorization.FileOperation{OperationID: mutation}, Name: "Secret installed file.txt", ObjectType: "job", ObjectID: j.ID}
	mutationIntent := []byte(fmt.Sprintf(`["file.rename",%d,{"operation_id":%q,"name":"Secret installed file.txt","revision":"","object_type":"job","object_id":%d}]`, file, mutation, j.ID))
	execSQL(`INSERT INTO file_mutation_operations(user_id,operation_id,client_key,intent_hash,file_id) VALUES($1,$2,$3,$5,$4)`, admin, mutation, hash, file, fmt.Sprintf("%x", sha256.Sum256(mutationIntent)))
	collision := add("cross_family_collision", "legacy.identity_conflict", "api:"+hash, "/comment-operations/", 200)
	removeIntent := []byte(fmt.Sprintf(`{"Type":"job","ID":%d,"Command":{"operation_id":%q}}`, j.ID, collision))
	removeDigest := fmt.Sprintf("%x", sha256.Sum256(append(removeIntent, []byte(fmt.Sprintf(":remove:%d", comment))...)))
	execSQL(`UPDATE comments SET removed_at=clock_timestamp(),removed_by=$1 WHERE id=$2`, admin, comment)
	execSQL(`INSERT INTO comment_operation_receipts(user_id,operation_id,client_key,intent_hash,comment_id,code) VALUES($1,$2,$3,$5,$4,'accepted')`, admin, collision, "api:"+hash, comment, removeDigest)
	collidingMutation := mutationCommand
	collidingMutation.OperationID = collision
	collidingIntent := []byte(fmt.Sprintf(`["file.rename",%d,{"operation_id":%q,"name":"Secret installed file.txt","revision":"","object_type":"job","object_id":%d}]`, file, collision, j.ID))
	execSQL(`INSERT INTO file_mutation_operations(user_id,operation_id,client_key,intent_hash,file_id) VALUES($1,$2,$3,$5,$4)`, admin, collision, hash, file, fmt.Sprintf("%x", sha256.Sum256(collidingIntent)))
	// Pre-registry transfers have resource UUIDs, not command receipts. Preserve
	// their actual session hash; do not invent an operation reservation for them.
	transfer := uuid.NewString()
	execSQL(`INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256,transferred) VALUES($1,$2,$3,$4,'job',$5,'Secret installed file.txt',11,'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9',11)`, transfer, admin, hash, publication, j.ID)
	execSQL(`INSERT INTO file_transfer_chunks(transfer_id,user_id,session_hash,offset_bytes,data) VALUES($1,$2,$3,0,$4)`, transfer, admin, hash, []byte("hello world"))
	snapshot := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM comment_operation_receipts x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id,client_hash) FROM job_field_operations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM commercial_operations x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM file_publications x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM file_mutation_operations x),(SELECT jsonb_agg(to_jsonb(x)) FROM file_transfers x),(SELECT jsonb_agg(to_jsonb(x)) FROM file_transfer_chunks x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM files x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM comments x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM estimates x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY client_id) FROM commercial_client_identities x))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		var fields string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('values',(SELECT custom_fields FROM jobs WHERE id=$1),'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY definition_id) FROM job_field_revisions r WHERE job_id=$1))::text`, j.ID).Scan(&fields); err != nil {
			t.Fatal(err)
		}
		return value + fields
	}
	before := snapshot()
	for range 2 {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
			t.Fatal(err)
		}
		if snapshot() != before {
			t.Fatal("upgrade rewrote installed receipts/effects/transport")
		}
	}
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20))
	scopeLost := false
	call := func(method, path, op, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Operation-ID", op)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body)
		}
		if scopeLost {
			for _, secret := range []string{"Secret installed", "hello world", "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", fileRoot, hash} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatalf("scope-loss response leaks %q: %s", secret, w.Body)
				}
			}
		}
		return w.Body.String()
	}
	if body := call("GET", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "", "", 200); !strings.Contains(body, "Secret installed field value") {
		t.Fatalf("authorized field control lacks source value: %s", body)
	}
	if body := call("GET", fmt.Sprintf("/files/%d", file), "", "", 200); body != "hello world" {
		t.Fatalf("authorized source File bytes: %q", body)
	}
	receiptBodies := map[string]string{}
	for _, tc := range cases {
		t.Logf("installed identity mapping: %s", tc.name)
		var kind, key string
		var absent bool
		if err := pool.QueryRow(ctx, `SELECT operation_type,client_key,intent IS NULL FROM operation_admissions WHERE operation_id=$1`, tc.id).Scan(&kind, &key, &absent); err != nil {
			t.Fatal(err)
		}
		if kind != tc.kind || key != tc.key || !absent {
			t.Fatalf("invented binding: %s %s null=%v", kind, key, absent)
		}
		receiptBody := call("GET", tc.lookup+tc.id, "", "", tc.status)
		if tc.status == 200 {
			if !strings.Contains(receiptBody, tc.id) {
				t.Fatalf("receipt lost original operation identity: %s", receiptBody)
			}
			receiptBodies[tc.id] = receiptBody
		}
		// A valid independent Comment is a second family for every row except
		// Comment itself, where a changed intent must still reject.
		call("POST", fmt.Sprintf("/jobs/%d/comments", j.ID), "", fmt.Sprintf(`{"operation_id":%q,"content":"must not execute"}`, tc.id), 409)
		// Header identity, rather than transfer resource identity, collides.
		body := fmt.Sprintf(`{"id":%q,"publication_id":%q,"object_type":"job","object_id":%d,"name":"second.txt","size":11,"sha256":"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"}`, uuid.NewString(), uuid.NewString(), j.ID)
		call("POST", "/file-transfers", tc.id, body, 409)
	}
	// Exact original Comment intent is still replayable without a second row.
	body, _ := json.Marshal(command)
	call("POST", fmt.Sprintf("/jobs/%d/comments", j.ID), "", string(body), 200)
	call("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "", string(fieldIntent), 200)
	call("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "", string(ambiguousIntent), 409)
	mutationBody, _ := json.Marshal(mutationCommand)
	call("POST", fmt.Sprintf("/files/%d/rename", file), "", string(mutationBody), 200)
	call("POST", "/file-transfers/"+transfer+"/publish", "", "", 200)
	call("GET", "/file-operations/"+collision, "", "", 200)
	collidingBody, _ := json.Marshal(collidingMutation)
	call("POST", fmt.Sprintf("/files/%d/rename", file), "", string(collidingBody), 409)
	call("POST", fmt.Sprintf("/jobs/%d/comments/%d/remove", j.ID, comment), "", fmt.Sprintf(`{"operation_id":%q}`, collision), 409)
	call("GET", "/file-transfers/"+transfer, "", "", 200)
	var invented int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions WHERE operation_id=$1`, transfer).Scan(&invented); err != nil || invented != 0 {
		t.Fatalf("invented transport operation: %d %v", invented, err)
	}
	if snapshot() != before {
		t.Fatal("replay/collision changed installed receipts or effects")
	}
	originalToken := token
	token, err = sessions.CreateMobile(ctx, admin, "Different current client")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		call("GET", tc.lookup+tc.id, "", "", 404)
	}
	for kind, intent := range commercialReplays {
		call("POST", "/"+kind+"s", "", intent, 409)
	}
	call("GET", "/file-transfers/"+transfer, "", "", 404)
	token = originalToken
	// Real scope loss: switch the actor to an explicitly empty nonprotected Role.
	var grants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_grants WHERE role_id=$1`, role).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("upgrade invented grants: %d %v", grants, err)
	}
	execSQL(`UPDATE users SET authorization_role_id=$1 WHERE id=$2`, role, admin)
	scopeLost = true
	for _, tc := range cases {
		body := call("GET", tc.lookup+tc.id, "", "", tc.status)
		if tc.status == 200 && body != receiptBodies[tc.id] {
			t.Fatalf("scope-loss changed original receipt: %s", body)
		}
		for _, secret := range []string{"Secret installed", "recorded publication intent", "recorded mutation digest", "values", "intent_hash", "file_path"} {
			if strings.Contains(body, secret) {
				t.Fatalf("scope-loss receipt leaks %q: %s", secret, body)
			}
		}
	}
	for _, tc := range cases {
		if tc.name == "estimate" || tc.name == "invoice" {
			call("POST", "/"+tc.name+"s", "", fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"title":""}`, tc.id, j.ID), 422)
		}
	}
	for kind, intent := range commercialReplays {
		call("POST", "/"+kind+"s", "", intent, 200)
	}
	call("GET", "/file-transfers/"+transfer, "", "", 404)
	call("GET", fmt.Sprintf("/files/%d", file), "", "", 404)
	call("POST", fmt.Sprintf("/files/%d/rename", file), "", string(mutationBody), 200)
	call("POST", fmt.Sprintf("/jobs/%d/comments", j.ID), "", string(body), 200)
	fieldReplay := call("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "", string(fieldIntent), 200)
	if strings.Contains(fieldReplay, "Secret installed") {
		t.Fatal("field replay disclosed target")
	}
	if snapshot() != before {
		t.Fatal("scope-loss lookup rewrote original evidence")
	}
	var shadowReceipts int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM file_command_receipts)+(SELECT count(*) FROM file_transport_receipts)`).Scan(&shadowReceipts); err != nil || shadowReceipts != 0 {
		t.Fatalf("legacy reservations acquired shadow receipts: %d %v", shadowReceipts, err)
	}
	var reservations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions`).Scan(&reservations); err != nil || reservations != len(cases) {
		t.Fatalf("unexpected additional reservations: %d want %d: %v", reservations, len(cases), err)
	}
	for _, table := range []string{"comment_operation_receipts", "job_field_operations", "commercial_operations", "file_publications", "file_mutation_operations", "operation_admissions"} {
		if _, err := pool.Exec(ctx, `UPDATE `+table+` SET operation_id=operation_id`); err == nil {
			t.Fatalf("%s permits historical rewrite", table)
		}
	}
}

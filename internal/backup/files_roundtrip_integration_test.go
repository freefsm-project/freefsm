package backup

import (
	"context"
	"golang.org/x/crypto/bcrypt"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the real migrated schema and encrypted cross-root backup engine,
// including an already removed File and pre-existing immutable Delivery bytes.
func TestFileHistoryEncryptedBackupRoundTrip(t *testing.T) {
	fixture := newCrashFixture(t)
	src, dst := fixture.manager(t, "file-source"), fixture.manager(t, "file-destination")
	ctx := context.Background()
	c, err := src.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var user, company, customer, estimate int64
	hash, err := bcrypt.GenerateFromPassword([]byte("FixtureOnly46!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	if err = c.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'file-roundtrip@test','Recorded uploader',$2,'admin') RETURNING id`, company, string(hash)).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = c.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Recorded parent') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err = c.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'Recorded estimate') RETURNING id`, company, customer).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, `INSERT INTO files(id,company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by,created_at,revision,removed_at,removed_by) VALUES(460046,$1,'estimate',$2,'recorded.txt','a.bin','text/plain',17,$3,$4,'2025-01-02T03:04:05Z',7,'2026-01-02T03:04:05Z',$4)`, company, estimate, filepath.Join(src.cfg.UploadDir, "a.bin"), user); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id) VALUES($1,'estimate',$2,$2,$3,'49304b16-11b4-404d-9951-94d884e80446',decode(repeat('00',32),'hex'),ARRAY['recipient@example.test'],'Recorded subject','Recorded body',decode('01020304','hex'),'captured.pdf','recorded-message-id')`, company, estimate, user); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err = c.QueryRow(ctx, `SELECT to_jsonb(f)-'file_path' FROM files f WHERE id=460046`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	op, err := src.StartBackup(user, "source", "file roundtrip password")
	if err != nil {
		t.Fatal(err)
	}
	src.wg.Wait()
	r, _, _, err := src.OpenDownload(user, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := dst.StageUpload(ctx, user, r, "file roundtrip password")
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	dst.wg.Wait()
	staged, err = dst.Status(user, staged.ID)
	if err != nil || staged.Phase != "ready" {
		t.Fatalf("preflight %+v %v", staged, err)
	}
	restored, err := dst.StartRestore(ctx, user, staged.ID, "destination", "destination")
	if err != nil {
		t.Fatal(err)
	}
	dst.wg.Wait()
	restored, err = dst.StatusCapability(restored.ID)
	if err != nil || restored.Phase != "complete" {
		t.Fatalf("restore %+v %v", restored, err)
	}
	d, err := dst.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close(ctx)
	var after []byte
	var path string
	if err = d.QueryRow(ctx, `SELECT to_jsonb(f)-'file_path',file_path FROM files f WHERE id=460046`).Scan(&after, &path); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("recorded File metadata changed: %s -> %s", before, after)
	}
	if path != filepath.Join(dst.cfg.UploadDir, "a.bin") {
		t.Fatalf("path was not rebased: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "file-source:a.bin" {
		t.Fatalf("removed File bytes: %q %v", data, err)
	}
	var snapshot bool
	if err = d.QueryRow(ctx, `SELECT pdf_data=decode('01020304','hex') AND pdf_filename='captured.pdf' AND message_id='recorded-message-id' FROM document_deliveries`).Scan(&snapshot); err != nil || !snapshot {
		t.Fatalf("Delivery snapshot changed: %v", err)
	}
}

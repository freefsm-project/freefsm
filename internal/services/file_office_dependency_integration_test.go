package services_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

func TestOfficeReadFailurePublicationRecovery(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "office_io_" + uuid.NewString()[:8]
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()
	pool, err := pgxpool.New(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, stdlib.OpenDB(*pool.Config().ConnConfig))))
	defer client.Close()
	var company, admin int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("OfficeIOFixture1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'office-io@test','Office reader',$2,'admin') RETURNING id`, company, string(hash)).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if _, err := services.NewSessionService(pool).CreateMobile(ctx, admin, "Office IO device"); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: admin, Channel: "api"}
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND kind='mobile'`, admin).Scan(&actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	ctx = authorization.WithActor(ctx, auth, actor)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Office IO Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Office IO Job").SaveX(ctx)
	svc := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20)
	ref := objectref.New(objectref.TypeJob, job.ID)
	for _, name := range []string{"document.doc", "workbook.xls", "slides.ppt"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "handlers", "testdata", "files", "legacy-office", name))
			if err != nil {
				t.Fatal(err)
			}
			readCount, err := services.OfficeReadCountForTest(data)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("injecting every one of %d underlying reads (header, compound setup, stream prefixes and full streams)", readCount)
			for read := 1; read <= readCount; read++ {
				t.Run(fmt.Sprintf("read-%d", read), func(t *testing.T) {
					cause := fmt.Errorf("storage read: %w", &os.PathError{Op: "pread", Path: "office payload", Err: syscall.EIO})
					injected, restore := services.OfficeReadFaultForTest(svc, read, cause)
					defer restore()
					digest := sha256.Sum256(data)
					publication := services.FilePublication{ID: uuid.NewString(), Digest: hex.EncodeToString(digest[:])}
					create := func() (*ent.File, error) {
						return svc.Create(ctx, company, ref, name, "", int64(len(data)), bytes.NewReader(data), admin, publication)
					}
					before := client.File.Query().CountX(ctx)
					_, err := create()
					if !injected() {
						t.Fatal("fault was not reached")
					}
					if !errors.Is(err, cause) {
						t.Errorf("underlying I/O lost: %v", err)
					}
					var terminal int
					if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM file_command_receipts WHERE operation_id=$1)+(SELECT count(*) FROM file_publications WHERE operation_id=$1)`, publication.ID).Scan(&terminal); err != nil {
						t.Fatal(err)
					}
					if terminal != 0 {
						t.Errorf("I/O failure consumed publication identity: %d rows", terminal)
					}
					if after := client.File.Query().CountX(ctx); after != before {
						t.Errorf("read failure published a File: %d -> %d", before, after)
					}
					if _, err := auth.FileReceipt(ctx, actor, publication.ID); !errors.Is(err, authorization.ErrNotFound) {
						t.Errorf("read failure receipt lookup: %v", err)
					}
					restore()
					accepted, err := create()
					if err != nil {
						t.Errorf("original UUID recovery failed: %v", err)
						return
					}
					replay, err := create()
					if err != nil || replay.ID != accepted.ID {
						t.Fatalf("same-ID replay: %v %v", replay, err)
					}
					if receipt, err := auth.FileReceipt(ctx, actor, publication.ID); err != nil || receipt.FileID != accepted.ID || receipt.Code != "accepted" {
						t.Fatalf("recovered receipt: %+v %v", receipt, err)
					}
					if after := client.File.Query().CountX(ctx); after != before+1 {
						t.Errorf("replay File count=%d, want %d", after, before+1)
					}
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_publications WHERE operation_id=$1`, publication.ID).Scan(&terminal); err != nil || terminal != 1 {
						t.Fatalf("accepted identity count=%d: %v", terminal, err)
					}
				})
			}
			// Real malformed bytes still consume one terminal identity, without a File.
			publication := services.FilePublication{ID: uuid.NewString()}
			for attempt := 0; attempt < 2; attempt++ {
				_, err := svc.Create(ctx, company, ref, name, "", 6, bytes.NewReader([]byte("broken")), admin, publication)
				var invalid *authorization.FileValidationError
				if !errors.As(err, &invalid) {
					t.Fatalf("malformed Office is not validation: %v", err)
				}
			}
			var code string
			if err := pool.QueryRow(ctx, `SELECT code FROM file_command_receipts WHERE operation_id=$1`, publication.ID).Scan(&code); err != nil || code != "invalid_file" {
				t.Fatalf("malformed receipt=%q: %v", code, err)
			}
		})
	}
}

package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Each adapter test owns a migrated schema in the explicitly supplied disposable DB.
func operationalExportDB(t *testing.T) (*pgxpool.Pool, authorization.Actor, int64) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("export98_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		root.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		root.Close()
	})
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var company, admin int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("ExportFixture98!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'export98@test','Exporter',$2,'admin') RETURNING id`, company, string(hash)).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	return pool, authorization.Actor{UserID: admin}, company
}

func TestJobOperationalDurableSnapshotAndArchiveRecheck(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	ctx := context.Background()
	var customer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	// More than a UI page; commas, quotes and line breaks must round-trip exactly.
	title := "Operational, \"quoted\"\nJob"
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) SELECT $1,$2,$3 FROM generate_series(1,105)`, company, customer, title); err != nil {
		t.Fatal(err)
	}
	svc := Product(pool)
	op := uuid.NewString()
	options := json.RawMessage(`{}`)
	id, err := svc.Accept(ctx, actor, op, "job-operational-A-v1", options)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := svc.Accept(ctx, actor, op, "job-operational-A-v1", options); err != nil || replay != id {
		t.Fatalf("replay: %d %v", replay, err)
	}
	if _, err := svc.Accept(ctx, actor, op, "job-operational-J-v1", options); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed intent: %v", err)
	}
	// Concurrent same-intent submissions converge on the same durable receipt.
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			replay, err := Product(pool).Accept(ctx, actor, op, "job-operational-A-v1", options)
			if err != nil || replay != id {
				t.Errorf("concurrent replay: %d %v", replay, err)
			}
		})
	}
	wg.Wait()
	// Intent acceptance is not the snapshot: generation observes the later title.
	title += " generation snapshot"
	if _, err := pool.Exec(ctx, `UPDATE jobs SET job_type=$1`, title); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	// Reconstructing the service models restart without an in-memory artifact.
	svc = Product(pool)
	download, err := svc.PrepareDownload(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(download).ReadAll()
	_ = download.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 106 {
		t.Fatalf("rows: %d", len(rows))
	}
	for _, row := range rows[1:] {
		if row[3] != title {
			t.Fatalf("title did not round trip: %q", row[3])
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=(SELECT min(id) FROM jobs)`); err != nil {
		t.Fatal(err)
	}
	if download, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrUnavailable) || download != nil {
		if download != nil {
			_ = download.Close()
		}
		t.Fatalf("archive disclosed artifact: %v", err)
	}
}

func TestJobOperationalAssignedExportDoesNotInheritAllView(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	var reader, customer, job int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'assigned98@test','Assigned reader','hash','tech') RETURNING id`, company).Scan(&reader); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, admin, 0, "Assigned exporter", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.export", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, admin, reader, role); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Restricted Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Assigned') RETURNING id`, company, customer).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Unassigned to requester')`, company, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job, reader); err != nil {
		t.Fatal(err)
	}
	svc := Product(pool)
	actor := authorization.Actor{UserID: reader}
	if _, err := svc.Accept(ctx, actor, uuid.NewString(), "job-operational-A-v1", json.RawMessage(`{}`)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("All export inherited View: %v", err)
	}
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "job-operational-J-v1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := svc.PrepareDownload(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(d).ReadAll()
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][3] != "Assigned" || rows[1][6] != "" {
		t.Fatalf("scope/facet leak: %v", rows)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job, reader); err != nil {
		t.Fatal(err)
	}
	if d, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrUnavailable) || d != nil {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("lost assignment: %v", err)
	}
}

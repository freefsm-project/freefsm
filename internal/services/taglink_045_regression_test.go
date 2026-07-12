package services_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestTagLinkServiceAttachAgainstSchemaThrough045Integration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	schemaName := fmt.Sprintf("freefsm_taglink_045_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schemaName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA `+schemaName+` CASCADE`) })

	schemaDSN := testSchemaDSN(t, dsn, schemaName)
	db, err := database.Connect(ctx, schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	through045 := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "046_" {
			continue
		}
		data, readErr := fs.ReadFile(database.MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through045[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through045); err != nil {
		t.Fatal(err)
	}

	schemaDB, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = schemaDB.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, schemaDB)))
	t.Cleanup(func() { _ = client.Close() })
	var companyID int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Tag owner','tag-owner') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	tag := client.Tag.Create().SetCompanyID(companyID).SetName("Priority").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Tagged Customer").SaveX(ctx)

	_, err = services.NewTagLinkService(client, objectref.NewEntDirectory(client)).Attach(ctx, companyID, tag.ID, objectref.New(objectref.TypeCustomer, customer.ID))
	if err != nil {
		t.Fatalf("Attach should set company ownership: %v", err)
	}
	if err = db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatalf("migrate 046: %v", err)
	}
	if err = services.NewTagService(client).Delete(ctx, companyID, tag.ID); !errors.Is(err, services.ErrTagConflict) {
		t.Fatalf("Delete linked tag error=%v, want ErrTagConflict", err)
	}
	var links int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, tag.ID).Scan(&links); err != nil || links != 1 {
		t.Fatalf("links after rejected delete=%d err=%v", links, err)
	}

	raceTag := client.Tag.Create().SetCompanyID(companyID).SetName("Race").SaveX(ctx)
	raceCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Race Target").SaveX(ctx)
	svc := services.NewTagLinkService(client, objectref.NewEntDirectory(client))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, attachErr := svc.Attach(ctx, companyID, raceTag.ID, objectref.New(objectref.TypeCustomer, raceCustomer.ID))
			errs <- attachErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for attachErr := range errs {
		switch {
		case attachErr == nil:
			successes++
		case errors.Is(attachErr, services.ErrTagConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Attach error=%v", attachErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func testSchemaDSN(t *testing.T, dsn, schemaName string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schemaName)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schemaName
}

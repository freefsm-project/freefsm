package services_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestCommentServiceCreatePersistsCompanyAgainstMigratedSchemaIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	schemaName := fmt.Sprintf("freefsm_comment_company_%d", time.Now().UnixNano())
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
	if err = db.Migrate(ctx, database.MigrationFS()); err != nil {
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
	if err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Comment owner','comment-owner') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	author := client.User.Create().SetCompanyID(companyID).SetEmail("comment-schema@example.test").SetPasswordHash("hash").SetName("Commenter").SetRole("dispatcher").SaveX(ctx)
	target := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Comment Target").SaveX(ctx)
	created, err := services.NewCommentService(client, objectref.NewEntDirectory(client)).Create(
		ctx, companyID, objectref.New(objectref.TypeCustomer, target.ID), author.ID, "persist ownership",
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var persistedCompanyID int64
	if err = db.Pool.QueryRow(ctx, `SELECT company_id FROM comments WHERE id=$1`, created.ID).Scan(&persistedCompanyID); err != nil {
		t.Fatal(err)
	}
	if persistedCompanyID != companyID {
		t.Fatalf("company_id=%d want %d", persistedCompanyID, companyID)
	}
	var otherCompanyID int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other comment owner','other-comment-owner') RETURNING id`).Scan(&otherCompanyID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($1,$2,$3,$4,'malformed')`, otherCompanyID, string(objectref.TypeCustomer), target.ID, author.ID); err != nil {
		t.Fatalf("insert mismatched-company comment: %v", err)
	}
	listed, err := services.NewCommentService(client, objectref.NewEntDirectory(client)).ListForObject(ctx, companyID, objectref.New(objectref.TypeCustomer, target.ID))
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("tenant list=%v err=%v; malformed row should be excluded", listed, err)
	}
}

package services

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/enttest"
	"github.com/freefsm-project/freefsm/internal/objectref"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPolicyServiceAssignmentAccessIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	policy := NewPolicyService(client, objectref.NewEntDirectory(client))
	now := time.Now()

	tech := client.User.Create().
		SetEmail("tech@example.test").
		SetPasswordHash("hash").
		SetName("Tech").
		SetRole("tech").
		SaveX(ctx)
	dispatcher := client.User.Create().
		SetEmail("dispatcher@example.test").
		SetPasswordHash("hash").
		SetName("Dispatcher").
		SetRole("dispatcher").
		SaveX(ctx)

	activeCustomer := client.Customer.Create().SetDisplayName("Active Customer").SaveX(ctx)
	archivedCustomer := client.Customer.Create().SetDisplayName("Archived Customer").SetDeletedAt(now).SaveX(ctx)
	directCustomer := client.Customer.Create().SetDisplayName("Direct Customer").SetAssignedTo(tech.ID).SaveX(ctx)
	archivedDirectCustomer := client.Customer.Create().SetDisplayName("Archived Direct Customer").SetAssignedTo(tech.ID).SetDeletedAt(now).SaveX(ctx)

	assetType := client.AssetType.Create().SetName("Equipment").SaveX(ctx)
	activeProject := client.Project.Create().SetCustomerID(activeCustomer.ID).SetName("Active Project").SaveX(ctx)
	archivedProject := client.Project.Create().SetCustomerID(activeCustomer.ID).SetName("Archived Project").SetDeletedAt(now).SaveX(ctx)
	activeAsset := client.Asset.Create().SetCustomerID(activeCustomer.ID).SetAssetTypeID(assetType.ID).SetName("Active Asset").SaveX(ctx)
	archivedAsset := client.Asset.Create().SetCustomerID(activeCustomer.ID).SetAssetTypeID(assetType.ID).SetName("Archived Asset").SetDeletedAt(now).SaveX(ctx)

	activeJob := client.Job.Create().
		SetCustomerID(activeCustomer.ID).
		SetProjectID(activeProject.ID).
		SetAssetID(activeAsset.ID).
		SetJobType("Active Job").
		SaveX(ctx)
	archivedJob := client.Job.Create().
		SetCustomerID(activeCustomer.ID).
		SetProjectID(activeProject.ID).
		SetAssetID(activeAsset.ID).
		SetJobType("Archived Job").
		SetDeletedAt(now).
		SaveX(ctx)
	archivedCustomerJob := client.Job.Create().
		SetCustomerID(archivedCustomer.ID).
		SetJobType("Archived Customer Job").
		SaveX(ctx)
	archivedProjectJob := client.Job.Create().
		SetCustomerID(activeCustomer.ID).
		SetProjectID(archivedProject.ID).
		SetJobType("Archived Project Job").
		SaveX(ctx)
	archivedAssetJob := client.Job.Create().
		SetCustomerID(activeCustomer.ID).
		SetAssetID(archivedAsset.ID).
		SetJobType("Archived Asset Job").
		SaveX(ctx)

	for _, jobID := range []int64{activeJob.ID, archivedJob.ID, archivedCustomerJob.ID, archivedProjectJob.ID, archivedAssetJob.ID} {
		client.JobAssignment.Create().SetJobID(jobID).SetUserID(tech.ID).SaveX(ctx)
	}

	tests := []struct {
		name string
		ref  objectref.Ref
		want bool
	}{
		{name: "tech can read active assigned job", ref: objectref.New(objectref.TypeJob, activeJob.ID), want: true},
		{name: "tech cannot read archived assigned job", ref: objectref.New(objectref.TypeJob, archivedJob.ID), want: false},
		{name: "tech can read active linked customer", ref: objectref.New(objectref.TypeCustomer, activeCustomer.ID), want: true},
		{name: "tech can read directly assigned active customer", ref: objectref.New(objectref.TypeCustomer, directCustomer.ID), want: true},
		{name: "tech cannot read archived linked customer", ref: objectref.New(objectref.TypeCustomer, archivedCustomer.ID), want: false},
		{name: "tech cannot read archived directly assigned customer", ref: objectref.New(objectref.TypeCustomer, archivedDirectCustomer.ID), want: false},
		{name: "tech can read active linked project", ref: objectref.New(objectref.TypeProject, activeProject.ID), want: true},
		{name: "tech cannot read archived linked project", ref: objectref.New(objectref.TypeProject, archivedProject.ID), want: false},
		{name: "tech can read active linked asset", ref: objectref.New(objectref.TypeAsset, activeAsset.ID), want: true},
		{name: "tech cannot read archived linked asset", ref: objectref.New(objectref.TypeAsset, archivedAsset.ID), want: false},
		{name: "tech cannot read estimates", ref: objectref.New(objectref.TypeEstimate, 1), want: false},
		{name: "tech cannot read invoices", ref: objectref.New(objectref.TypeInvoice, 1), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.CanAccessObject(ctx, tech.ID, tech.Role, tt.ref, PolicyRead)
			if got != tt.want {
				t.Fatalf("CanAccessObject(%v) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}

	archivedJobRef := objectref.New(objectref.TypeJob, archivedJob.ID)
	if !policy.CanAccessObject(ctx, dispatcher.ID, dispatcher.Role, archivedJobRef, PolicyRead) {
		t.Fatal("dispatcher read archived job = false, want true")
	}
	if policy.CanAccessObject(ctx, dispatcher.ID, dispatcher.Role, archivedJobRef, PolicyCreate) {
		t.Fatal("dispatcher create on archived job = true, want false")
	}
	if policy.CanAccessObject(ctx, dispatcher.ID, dispatcher.Role, archivedJobRef, PolicyDelete) {
		t.Fatal("dispatcher delete on archived job = true, want false")
	}
}

func openPolicyTestClient(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run Postgres policy integration tests")
	}

	schemaName := fmt.Sprintf("freefsm_policy_test_%d", time.Now().UnixNano())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`)
	})

	schemaDSN, err := dsnWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatalf("set test search_path: %v", err)
	}
	schemaDB, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	t.Cleanup(func() { _ = schemaDB.Close() })

	return enttest.NewClient(t, enttest.WithOptions(ent.Driver(entsql.OpenDB(dialect.Postgres, schemaDB))))
}

func dsnWithSearchPath(dsn, schemaName string) (string, error) {
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		q := u.Query()
		q.Set("search_path", schemaName)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return strings.TrimSpace(dsn) + " search_path=" + schemaName, nil
}

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
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPolicyServiceAssignmentAccessIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	policy := NewPolicyService(client)
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
		name       string
		objectType string
		objectID   int64
		want       bool
	}{
		{name: "tech can read active assigned job", objectType: "job", objectID: activeJob.ID, want: true},
		{name: "tech cannot read archived assigned job", objectType: "job", objectID: archivedJob.ID, want: false},
		{name: "tech can read active linked customer", objectType: "customer", objectID: activeCustomer.ID, want: true},
		{name: "tech can read directly assigned active customer", objectType: "customer", objectID: directCustomer.ID, want: true},
		{name: "tech cannot read archived linked customer", objectType: "customer", objectID: archivedCustomer.ID, want: false},
		{name: "tech cannot read archived directly assigned customer", objectType: "customer", objectID: archivedDirectCustomer.ID, want: false},
		{name: "tech can read active linked project", objectType: "project", objectID: activeProject.ID, want: true},
		{name: "tech cannot read archived linked project", objectType: "project", objectID: archivedProject.ID, want: false},
		{name: "tech can read active linked asset", objectType: "asset", objectID: activeAsset.ID, want: true},
		{name: "tech cannot read archived linked asset", objectType: "asset", objectID: archivedAsset.ID, want: false},
		{name: "tech cannot read estimates", objectType: "estimate", objectID: 1, want: false},
		{name: "tech cannot read invoices", objectType: "invoice", objectID: 1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.CanAccessObject(ctx, tech.ID, tech.Role, tt.objectType, tt.objectID, "read")
			if got != tt.want {
				t.Fatalf("CanAccessObject(%q, %d) = %v, want %v", tt.objectType, tt.objectID, got, tt.want)
			}
		})
	}

	if !policy.CanAccessObject(ctx, dispatcher.ID, dispatcher.Role, "job", archivedJob.ID, "read") {
		t.Fatal("dispatcher read archived job = false, want true")
	}
	if policy.CanAccessObject(ctx, dispatcher.ID, dispatcher.Role, "job", archivedJob.ID, "create") {
		t.Fatal("dispatcher create on archived job = true, want false")
	}
	if policy.CanAccessObject(ctx, dispatcher.ID, dispatcher.Role, "job", archivedJob.ID, "delete") {
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

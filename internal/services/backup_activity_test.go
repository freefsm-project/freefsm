package services

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/ent/activitylog"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestBackupActivityHistoricalActorAndReplay(t *testing.T) {
	fixture := openActivityConversionTestClient(t)
	ctx := context.Background()
	client := fixture.client
	const companyID int64 = 42
	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Restored company").SaveX(ctx)
	unrelated := client.User.Create().SetCompanyID(companyID).SetName("Unrelated restored user").SetEmail("restored@example.test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	event := backup.ActivityEvent{Key: "restore-outcome-test", Action: "restore_completed", OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC), Actor: backup.ActivityActor{ID: unrelated.ID, CompanyID: 9876, Name: "Original administrator", CompanyName: "Destination before restore"}, SourceName: "Backup source"}
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	var schema string
	if err := fixture.db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn, err := dsnWithSearchPath(os.Getenv("FREEFSM_TEST_DATABASE_URL"), schema)
	if err != nil {
		t.Fatal(err)
	}
	// Recovery uses independently opened connections and the same key. Concurrent
	// insertion and subsequent replay must leave one immutable historical fact.
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() { results <- WriteBackupActivity(ctx, dsn, event) }()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := WriteBackupActivity(ctx, dsn, event); err != nil {
			t.Fatal(err)
		}
	}
	row := client.ActivityLog.Query().Where(activitylog.EventKeyEQ(event.Key)).OnlyX(ctx)
	if row.ActorID != nil || row.CompanyID != companyID || !row.CreatedAt.Equal(event.OccurredAt) {
		t.Fatalf("historical identity/time not preserved: %+v", row)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(row.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["actor_name"] != event.Actor.Name || metadata["actor_company_name"] != event.Actor.CompanyName {
		t.Fatalf("lost snapshot: %v", metadata)
	}
	for _, admin := range []bool{false, true} {
		page, err := svc.List(ctx, ActivityListRequest{CompanyID: companyID, Scope: TenantActivityScope{IncludeAdminOnly: admin}})
		if err != nil {
			t.Fatal(err)
		}
		if (len(page.Entries) == 1) != admin {
			t.Fatalf("admin-only activity: admin=%v entries=%v", admin, page.Entries)
		}
	}
}

func TestBackupActivityMetadataAllowlist(t *testing.T) {
	event := backup.ActivityEvent{Key: "private-event-key", Action: "restore_failed", OccurredAt: time.Now(), Actor: backup.ActivityActor{ID: 71, CompanyID: 91, Name: "Original administrator"}, FailureCategory: "storage", FailurePhase: "install", FailureMessage: "Not enough disk space", DiagnosticID: "safe-diagnostic-id"}
	encoded, err := backupActivityMetadata(event)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]string
	if err := json.Unmarshal([]byte(encoded), &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 6 || metadata["failure_message"] != event.FailureMessage || metadata["diagnostic_id"] != event.DiagnosticID {
		t.Fatalf("unexpected metadata: %s", encoded)
	}
	for _, forbidden := range []string{"key", "event_key", "operation", "actor_id", "company_id", "password", "archive_path"} {
		if _, ok := metadata[forbidden]; ok {
			t.Fatalf("unexpected metadata field: %s", forbidden)
		}
	}
}

func TestBackupActivityRejectsIncompleteFacts(t *testing.T) {
	for _, event := range []backup.ActivityEvent{{}, {Key: "key", Action: "unknown", OccurredAt: time.Now(), Actor: backup.ActivityActor{Name: "Admin"}}} {
		if _, err := backupActivityMetadata(event); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
}

func TestBackupActivityResolverDoesNotResolveHistoricalActorIDs(t *testing.T) {
	resolver := NewActivityResolver(nil)
	for _, role := range []string{"admin", "dispatcher", "tech"} {
		result, err := resolver.Resolve(context.Background(), 42, ActivityViewer{ID: 7, Role: role}, []ActivityEntry{{Target: objectref.Instance(), Metadata: `{"actor_name":"Historical administrator"}`}})
		if err != nil || result.ActorErr != nil || len(result.ActorNames) != 0 {
			t.Fatalf("historical actor triggered a user lookup: %+v, %v", result, err)
		}
		target := result.Targets[objectref.Instance()]
		if target.Err != nil || !target.Exists || target.Readable != (role == "admin") || (target.URL != "") != (role == "admin") {
			t.Fatalf("incorrect instance access for %s: %+v", role, target)
		}
	}
}

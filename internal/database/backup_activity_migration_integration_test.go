package database

import (
	"io/fs"
	"os"
	"testing"
)

func TestBackupActivityMigration054HistoricalActorsAndEventKeys(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL migration tests")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var companyID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO companies(name, slug) VALUES ('Restored', 'restored') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO activity_logs(company_id, actor_id, event_key, action, object_type, object_id, metadata)
		VALUES ($1, NULL, $2, 'restore_completed', 'instance', 1, '{"actor_name":"Historical administrator"}')`
	if _, err := db.Pool.Exec(ctx, insert, companyID, "replay-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, insert, companyID, "replay-key"); err == nil {
		t.Fatal("duplicate event key accepted")
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Pool.Exec(ctx, insert, companyID, nil); err != nil {
			t.Fatalf("ordinary rows may omit event key: %v", err)
		}
	}
	down, err := fs.ReadFile(MigrationFS(), "054_backup_activity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("downgrade silently removed historical actors")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE actor_id IS NULL`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("history was modified by downgrade: count=%d err=%v", count, err)
	}
}

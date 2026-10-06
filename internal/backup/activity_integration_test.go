package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/services"
)

func seedActivityActor(t *testing.T, m *Manager, id int64, name string) ActivityActor {
	t.Helper()
	c, err := m.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	_, err = c.Exec(context.Background(), `INSERT INTO companies(id,name,slug) VALUES($1,$2,$2) ON CONFLICT(id) DO UPDATE SET name=excluded.name,slug=excluded.slug`, id, name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exec(context.Background(), `UPDATE company_settings SET company_id=$1,business_name=$2`, id, name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exec(context.Background(), `INSERT INTO users(id,company_id,email,password_hash,name,role) VALUES($1,$1,$2,'unused',$2,'admin')`, id, name)
	if err != nil {
		t.Fatal(err)
	}
	return ActivityActor{ID: id, CompanyID: id, Name: name, CompanyName: name}
}

func TestRestoreActivityDurablePublication(t *testing.T) {
	fixture := newCrashFixture(t)
	ctx := context.Background()
	src := fixture.manager(t, "source")
	sourceActor := seedActivityActor(t, src, 1, "Archive actor")
	old := ActivityEvent{Key: "archived-history", Action: "backup_created", Actor: sourceActor, OccurredAt: time.Now().UTC()}
	if err := services.WriteBackupActivity(ctx, src.cfg.DSN, old); err != nil {
		t.Fatal(err)
	}
	archive := createCrashArchive(t, src)
	for _, scenario := range []string{"collision-publication-failure", "missing-actor-postpublication-crash", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			dst := fixture.manager(t, "destination")
			id := int64(1)
			if scenario == "missing-actor-postpublication-crash" {
				id = 7
			}
			actor := seedActivityActor(t, dst, id, "Destination administrator")
			fail := false
			dst.cfg.ActivitySink = func(ctx context.Context, event ActivityEvent) error {
				if fail && event.Action == "restore_completed" {
					return errors.New("secret connection password")
				}
				return services.WriteBackupActivity(ctx, dst.cfg.DSN, event)
			}
			prior := old
			prior.Key = "unrelated-destination"
			prior.Actor = actor
			if err := dst.cfg.ActivitySink(ctx, prior); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(archive)
			if err != nil {
				t.Fatal(err)
			}
			op, err := dst.StageUploadFor(ctx, actor, f, crashArchivePassword)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			op = awaitOperation(t, dst, actor.ID, op.ID)
			if op.Phase != "ready" {
				t.Fatalf("stage: %+v", op)
			}
			fail = scenario == "collision-publication-failure"
			dst.checkpoint = func(point string) error {
				if scenario == "rollback" && point == "protections-applied" || scenario == "missing-actor-postpublication-crash" && point == "activity-published" {
					return errors.New("interruption")
				}
				return nil
			}
			op, err = dst.StartRestoreFor(ctx, actor, op.ID, "destination", "destination")
			if err != nil {
				t.Fatal(err)
			}
			op = awaitOperation(t, dst, actor.ID, op.ID)
			if scenario != "rollback" && op.Phase != "complete" {
				t.Fatalf("committed restore marked failed: %+v", op)
			}
			if scenario == "rollback" && op.Phase != "failed" {
				t.Fatalf("rollback status: %+v", op)
			}
			var durable journal
			if scenario != "rollback" {
				b, err := os.ReadFile(dst.journalPath())
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(b, &durable); err != nil {
					t.Fatal(err)
				}
				if durable.Phase != "committed" {
					t.Fatalf("journal: %+v", durable)
				}
				if _, err := os.Stat(filepath.Join(dst.root, durable.Recovery, "database.dump")); err != nil {
					t.Fatal("last recovery copy removed", err)
				}
			}
			fail = false
			dst.checkpoint = nil
			for i := 0; i < 2; i++ {
				if err := dst.Recover(ctx); err != nil {
					t.Fatal(err)
				}
			}
			c, err := dst.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(ctx)
			rows, err := c.Query(ctx, `SELECT event_key, action, actor_id, company_id, metadata->>'actor_name', created_at FROM activity_logs WHERE action LIKE 'restore_%' ORDER BY created_at`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var key, action, name string
				var actorID *int64
				var company int64
				var occurred time.Time
				if err := rows.Scan(&key, &action, &actorID, &company, &name, &occurred); err != nil {
					t.Fatal(err)
				}
				if key == op.ID || actorID != nil || name != actor.Name {
					t.Fatalf("unsafe identity: %s %v %s", key, actorID, name)
				}
				if company != 1 {
					t.Fatalf("wrong current company: %d", company)
				}
				if scenario != "rollback" && action == "restore_completed" && (key != durable.Outcome.Key || !occurred.Equal(durable.Outcome.OccurredAt.Truncate(time.Microsecond))) {
					t.Fatal("replay changed event identity/time")
				}
				count++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Fatalf("restore rows: %d", count)
			}
			var priorCount, archivedCount int
			if err := c.QueryRow(ctx, `SELECT count(*) FILTER (WHERE event_key='unrelated-destination'),count(*) FILTER (WHERE event_key='archived-history') FROM activity_logs`).Scan(&priorCount, &archivedCount); err != nil {
				t.Fatal(err)
			}
			if scenario == "rollback" {
				if priorCount != 1 || archivedCount != 0 {
					t.Fatal("rollback history changed")
				}
			} else if priorCount != 0 || archivedCount != 1 {
				t.Fatal("restore merged destination history or lost archive history")
			}
		})
	}
}

func TestRestoreActivitySIGKILLReplay(t *testing.T) {
	fixture := newCrashFixture(t)
	ctx := context.Background()
	src := fixture.manager(t, "source")
	seedActivityActor(t, src, 1, "Archive actor")
	archive := createCrashArchive(t, src)
	for _, point := range []string{"committed-durable", "activity-published", "protections-applied"} {
		t.Run(point, func(t *testing.T) {
			dst := fixture.manager(t, "destination")
			seedActivityActor(t, dst, 1, "Destination administrator")
			dst.cfg.ActivitySink = func(ctx context.Context, event ActivityEvent) error {
				return services.WriteBackupActivity(ctx, dst.cfg.DSN, event)
			}
			runCrashChild(t, dst, archive, "restore", point)
			b, err := os.ReadFile(dst.journalPath())
			if err != nil {
				t.Fatal(err)
			}
			var state journal
			if err := json.Unmarshal(b, &state); err != nil {
				t.Fatal(err)
			}
			if state.Start == nil || state.Outcome == nil {
				t.Fatal("missing durable actor evidence")
			}
			if state.Outcome.DiagnosticID != "" {
				t.Fatalf("placeholder/success must not invent a diagnostic: %+v", state.Outcome)
			}
			// Replay in a fresh development-build process, before migrations.
			runCrashChild(t, dst, "", "recover", "")
			runCrashChild(t, dst, "", "recover", "")
			c, err := dst.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(ctx)
			var started, completed, failed int
			if err := c.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='restore_started'),count(*) FILTER(WHERE action='restore_completed'),count(*) FILTER(WHERE action='restore_failed') FROM activity_logs`).Scan(&started, &completed, &failed); err != nil {
				t.Fatal(err)
			}
			if started != 1 || point == "protections-applied" && (completed != 0 || failed != 1) || point != "protections-applied" && (completed != 1 || failed != 0) {
				t.Fatalf("duplicate/wrong outcomes: %d %d %d", started, completed, failed)
			}
			var key, name string
			if err := c.QueryRow(ctx, `SELECT event_key,metadata->>'actor_name' FROM activity_logs WHERE action='restore_started'`).Scan(&key, &name); err != nil {
				t.Fatal(err)
			}
			if key != state.Start.Key || name != state.Start.Actor.Name {
				t.Fatal("crash changed snapshot")
			}
		})
	}
}

func TestBackupActivityDownloadAndValidation(t *testing.T) {
	fixture := newCrashFixture(t)
	m := fixture.manager(t, "source")
	ctx := context.Background()
	actor := seedActivityActor(t, m, 1, "Administrator")
	fail := false
	m.cfg.ActivitySink = func(ctx context.Context, event ActivityEvent) error {
		if fail {
			return errors.New("sink unavailable")
		}
		return services.WriteBackupActivity(ctx, m.cfg.DSN, event)
	}
	op, err := m.StartBackupFor(actor, "source", crashArchivePassword)
	if err != nil {
		t.Fatal(err)
	}
	op = awaitOperation(t, m, actor.ID, op.ID)
	if op.Phase != "complete" {
		t.Fatalf("backup: %+v", op)
	}
	if err := m.RecordDownload(ctx, actor, op.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("recorded unopened download", err)
	}
	r, _, _, err := m.OpenDownload(actor.ID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fail = true
	if err := m.RecordDownload(ctx, actor, op.ID); err == nil {
		t.Fatal("failed sink permitted download")
	}
	fail = false
	if err := m.RecordDownload(ctx, actor, op.ID); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(actor.ID, op.ID)
	if err != nil || status.Phase != "complete" {
		t.Fatal("download failure changed backup", status, err)
	}
	upload, err := m.StageUploadFor(ctx, actor, r, "wrong-password")
	if err != nil {
		t.Fatal(err)
	}
	upload = awaitOperation(t, m, actor.ID, upload.ID)
	if upload.Phase != "failed" {
		t.Fatalf("validation: %+v", upload)
	}
	c, err := m.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var created, downloaded, invalid int
	if err := c.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='backup_created'),count(*) FILTER(WHERE action='backup_download_started'),count(*) FILTER(WHERE action='backup_validation_failed') FROM activity_logs`).Scan(&created, &downloaded, &invalid); err != nil {
		t.Fatal(err)
	}
	if created != 1 || downloaded != 1 || invalid != 1 {
		t.Fatalf("outcomes %d %d %d", created, downloaded, invalid)
	}
}

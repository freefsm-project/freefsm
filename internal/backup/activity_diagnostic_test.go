package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/services"
)

func readActivityDiagnostic(t *testing.T, m *Manager) Diagnostic {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(m.root, "last-failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d Diagnostic
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestActivityFailureDiagnosticCorrelation(t *testing.T) {
	fixture := newCrashFixture(t)
	ctx := context.Background()
	src := fixture.manager(t, "source")
	seedActivityActor(t, src, 1, "Archive administrator")
	archive := createCrashArchive(t, src)
	for _, scenario := range []struct {
		kind             string
		publicationFails bool
	}{{"backup", false}, {"validation", false}, {"restore", false}, {"backup", true}, {"validation", true}, {"restore", true}} {
		name := scenario.kind
		if scenario.publicationFails {
			name += "-publication-failure"
		}
		t.Run(name, func(t *testing.T) {
			kind := scenario.kind
			m := fixture.manager(t, "destination")
			actor := seedActivityActor(t, m, 1, "Administrator")
			var events []ActivityEvent
			var reports []Diagnostic
			publicationUnavailable := scenario.publicationFails
			m.cfg.ActivitySink = func(ctx context.Context, event ActivityEvent) error {
				events = append(events, event)
				if publicationUnavailable && strings.HasSuffix(event.Action, "_failed") {
					return errors.New("activity publication unavailable")
				}
				return services.WriteBackupActivity(ctx, m.cfg.DSN, event)
			}
			m.cfg.ReportFailure = func(d Diagnostic) { reports = append(reports, d) }
			var op Operation
			var err error
			wantCategory := FailureDatabaseExtraSchemas
			wantAction := "backup_failed"
			if kind == "backup" {
				c, err := m.connect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.Exec(ctx, "CREATE SCHEMA unsupported")
				_ = c.Close(ctx)
				if err != nil {
					t.Fatal(err)
				}
				op, err = m.StartBackupFor(actor, "source", crashArchivePassword)
			} else {
				password := crashArchivePassword
				if kind == "validation" {
					password = "incorrect-archive-password"
					wantCategory, wantAction = FailureArchiveAuthentication, "backup_validation_failed"
				}
				f, openErr := os.Open(archive)
				if openErr != nil {
					t.Fatal(openErr)
				}
				op, err = m.StageUploadFor(ctx, actor, f, password)
				_ = f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			op = awaitOperation(t, m, actor.ID, op.ID)
			if kind == "restore" {
				if op.Phase != "ready" {
					t.Fatalf("stage: %+v", op)
				}
				wantCategory, wantAction = FailureCancelled, "restore_failed"
				m.checkpoint = func(point string) error {
					if point == "protections-applied" {
						return context.Canceled
					}
					return nil
				}
				op, err = m.StartRestoreFor(ctx, actor, op.ID, "destination", "destination")
				if err != nil {
					t.Fatal(err)
				}
				op = awaitOperation(t, m, actor.ID, op.ID)
			}
			wantStatusCategory, wantReports := wantCategory, 1
			if scenario.publicationFails {
				wantStatusCategory, wantReports = FailureRecoveryRequired, 2
			}
			if op.Phase != "failed" || op.Failure.Category != wantStatusCategory || op.Failure.DiagnosticID == "" {
				t.Fatalf("failure status: %+v", op)
			}
			if len(reports) != wantReports || reports[len(reports)-1].Failure != op.Failure {
				t.Fatalf("expected one diagnostic per failure, latest shared with status: reports=%+v status=%+v", reports, op.Failure)
			}
			original := reports[0].Failure
			if original.Category != wantCategory {
				t.Fatalf("original cause lost: %+v", original)
			}
			if scenario.publicationFails && original.DiagnosticID == op.Failure.DiagnosticID {
				t.Fatal("publication failure reused original cause's diagnostic")
			}
			if durable := readActivityDiagnostic(t, m); durable.Failure != op.Failure {
				t.Fatalf("durable/status mismatch: %+v %+v", durable.Failure, op.Failure)
			}
			var event ActivityEvent
			for _, candidate := range events {
				if candidate.Action == wantAction {
					event = candidate
				}
			}
			if event.DiagnosticID != original.DiagnosticID || event.FailureCategory != string(original.Category) || event.FailurePhase != original.Phase || event.FailureMessage != original.Message {
				t.Fatalf("activity must reuse the exact original failure, even when publication fails: event=%+v original=%+v", event, original)
			}
			if scenario.publicationFails {
				publicationUnavailable = false
				m.checkpoint = nil
				for i := 0; i < 2; i++ {
					if err := m.Recover(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if len(reports) != wantReports {
					t.Fatal("successful replay re-reported a historical failure")
				}
			}
			c, err := m.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(ctx)
			var storedID string
			if err := c.QueryRow(ctx, `SELECT metadata->>'diagnostic_id' FROM activity_logs WHERE event_key=$1`, event.Key).Scan(&storedID); err != nil {
				t.Fatal(err)
			}
			if storedID != original.DiagnosticID {
				t.Fatalf("stored activity diagnostic %q differs from original %q", storedID, original.DiagnosticID)
			}
		})
	}
}

type activityReaderFunc func([]byte) (int, error)

func (f activityReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestInterruptedActivityHasNoDiagnostic(t *testing.T) {
	m := storageManager(t)
	var event ActivityEvent
	var reports []Diagnostic
	m.cfg.ActivitySink = func(_ context.Context, e ActivityEvent) error { event = e; return nil }
	m.cfg.ReportFailure = func(d Diagnostic) { reports = append(reports, d) }
	input := activityReaderFunc(func([]byte) (int, error) {
		b, err := os.ReadFile(m.pendingPath())
		if err != nil {
			t.Fatal(err)
		}
		var pending pendingActivity
		if err := json.Unmarshal(b, &pending); err != nil {
			t.Fatal(err)
		}
		if pending.Event.DiagnosticID != "" || len(reports) != 0 {
			t.Errorf("interrupted placeholder must not invent a diagnostic: %+v reports=%+v", pending.Event, reports)
		}
		return 0, context.Canceled
	})
	_, err := m.StageUploadFor(context.Background(), ActivityActor{ID: 1, Name: "Administrator"}, input, "secret")
	if err == nil {
		t.Fatal("upload should fail")
	}
	if len(reports) != 1 || event.DiagnosticID == "" || event.DiagnosticID != reports[0].Failure.DiagnosticID || event.FailurePhase != "uploading" {
		t.Fatalf("actual read failure must share its reported diagnostic: %+v reports=%+v", event, reports)
	}
	if durable := readActivityDiagnostic(t, m); durable.Failure != reports[0].Failure {
		t.Fatal("read failure diagnostic changed")
	}
}

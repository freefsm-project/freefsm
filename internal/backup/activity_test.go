package backup

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPendingActivityBlocksNewWorkAndReplaysExactly(t *testing.T) {
	m := storageManager(t)
	actor := ActivityActor{ID: 1, Name: "Administrator", CompanyName: "Company"}
	var first ActivityEvent
	m.cfg.ActivitySink = func(_ context.Context, event ActivityEvent) error {
		first = event
		return errors.New("secret-password /private/path")
	}
	if _, err := m.StartBackup(1, "source", "archive-secret"); !errors.Is(err, ErrInvalid) {
		t.Fatal("legacy wrapper accepted missing snapshot", err)
	}
	op, err := m.StartBackupFor(actor, "source", "archive-secret")
	if err != nil {
		t.Fatal(err)
	}
	op = awaitOperation(t, m, actor.ID, op.ID)
	if op.Phase != "failed" || op.Failure.Category != FailureRecoveryRequired {
		t.Fatalf("status: %+v", op)
	}
	if first.Action != "backup_failed" || first.Actor != actor || first.Key == op.ID || first.Key == "" {
		t.Fatalf("event: %+v", first)
	}
	if _, err := m.StartBackupFor(actor, "source", "secret"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("pending publication superseded", err)
	}
	b, err := os.ReadFile(m.pendingPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"archive-secret", "secret-password", "/private/path", op.ID} {
		if strings.Contains(string(b), secret) {
			t.Fatal("pending evidence leaks secret")
		}
	}
	calls := 0
	m.cfg.ActivitySink = func(_ context.Context, event ActivityEvent) error {
		calls++
		if !reflect.DeepEqual(first, event) {
			t.Fatal("retry changed immutable event", first, event)
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := m.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("replayed %d times", calls)
	}
}

func TestUploadReadFailurePublishesOutcome(t *testing.T) {
	m := storageManager(t)
	var got ActivityEvent
	m.cfg.ActivitySink = func(_ context.Context, event ActivityEvent) error { got = event; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.StageUploadFor(ctx, ActivityActor{ID: 1, Name: "Administrator"}, strings.NewReader("incomplete"), "secret")
	if err == nil || got.Action != "backup_validation_failed" || got.FailureCategory != "cancelled" {
		t.Fatalf("read failure: %+v %v", got, err)
	}
	if _, err := os.Stat(m.pendingPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published outcome retained", err)
	}
}

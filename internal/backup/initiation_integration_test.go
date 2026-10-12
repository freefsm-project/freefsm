package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestInitiationCapabilityReconcilesRealBackupUploadAndRestart(t *testing.T) {
	f := newCrashFixture(t)
	m := f.manager(t, "guarded initiation")
	id := strings.Repeat("a", 64)
	before, err := m.InitiationStatus(id)
	if err != nil || before.Disposition != "not_received" {
		t.Fatalf("before %+v %v", before, err)
	}
	op, err := m.StartBackupAction(ActivityActor{ID: 1}, id, "source", crashArchivePassword)
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != id {
		t.Fatalf("not prebound: %s", op.ID)
	}
	if _, err := m.StartBackupAction(ActivityActor{ID: 1}, id, "source", crashArchivePassword); !errors.Is(err, ErrInitiated) {
		t.Fatalf("duplicate: %v", err)
	}
	op = awaitOperation(t, m, 1, id)
	if op.Phase != "complete" {
		t.Fatalf("real backup %+v", op)
	}
	r, _, _, err := m.OpenDownload(1, id)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	uploadID := strings.Repeat("b", 64)
	if _, err := m.StageUploadAction(context.Background(), ActivityActor{ID: 1}, uploadID, strings.NewReader(string(bytes)), crashArchivePassword); err != nil {
		t.Fatal(err)
	}
	up := awaitOperation(t, m, 1, uploadID)
	if up.Phase != "ready" {
		t.Fatalf("real validation %+v", up)
	}
	if _, err := m.StageUploadAction(context.Background(), ActivityActor{ID: 1}, uploadID, panicUploadReader{}, crashArchivePassword); !errors.Is(err, ErrInitiated) {
		t.Fatalf("upload redispatch %v", err)
	}
	status, err := m.InitiationStatus(uploadID)
	if err != nil || status.Disposition != "ready" || !status.Available {
		t.Fatalf("ready receipt %+v %v", status, err)
	}
	m.Shutdown()
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown()
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = restarted.InitiationStatus(id)
	if err != nil || status.Disposition != "complete" || status.Available {
		t.Fatalf("durable completion %+v %v", status, err)
	}
	if _, err := restarted.StartBackupAction(ActivityActor{ID: 1}, id, "source", crashArchivePassword); !errors.Is(err, ErrInitiated) {
		t.Fatalf("restart redispatch %v", err)
	}
}

type panicUploadReader struct{}

func (panicUploadReader) Read([]byte) (int, error) {
	panic("duplicate upload body must not be consumed")
}

func TestInitiationInterruptedResultRemainsUnknownAfterRealBackupRestart(t *testing.T) {
	m := newCrashFixture(t).manager(t, "unknown initiation")
	m.checkpoint = func(point string) error {
		if point == "initiation-result" {
			return errors.New("injected host receipt persistence failure")
		}
		return nil
	}
	id := strings.Repeat("c", 64)
	op, err := m.StartBackupAction(ActivityActor{ID: 1}, id, "source", crashArchivePassword)
	if err != nil {
		t.Fatal(err)
	}
	op = awaitOperation(t, m, 1, op.ID)
	if op.Phase != "complete" {
		t.Fatalf("effect did not complete %+v", op)
	}
	r, _, size, err := m.OpenDownload(1, id)
	if err != nil || size == 0 {
		t.Fatalf("actual encrypted effect %d %v", size, err)
	}
	r.Close()
	m.Shutdown()
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown()
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	receipt, err := restarted.InitiationStatus(id)
	if err != nil || receipt.Disposition != "unknown" || receipt.Available {
		t.Fatalf("unknown restart %+v %v", receipt, err)
	}
	if _, err := restarted.StartBackupAction(ActivityActor{ID: 1}, id, "changed input", crashArchivePassword); !errors.Is(err, ErrInitiated) {
		t.Fatalf("unknown redispatched %v", err)
	}
}

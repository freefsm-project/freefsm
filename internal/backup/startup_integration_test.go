package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/testutil"
)

func TestAdmissionStartupOwnsInitializationUntilVerifiedFinish(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.InstanceDatabase(t)
	root := t.TempDir()
	c, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DSN: dsn, UploadDir: filepath.Join(root, "uploads"), StateDir: c.StateDir(), Control: c, BuildKind: BuildDevelopment}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	startup, err := m.BeginStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer startup.Close()
	peer, err := instancecontrol.New(c.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	assertClosed := func() {
		t.Helper()
		if _, release, err := peer.Enter(ctx); !errors.Is(err, instancecontrol.ErrMaintenance) {
			if release != nil {
				release()
			}
			t.Fatalf("initialization admitted: %v", err)
		}
		if unlock, err := peer.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
			if unlock != nil {
				unlock()
			}
			t.Fatalf("initialization lost operation reservation: %v", err)
		}
	}
	assertClosed()
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.UploadDir, 0700); err != nil {
		t.Fatal(err)
	}
	assertClosed()
	if err := startup.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := startup.Finish(ctx); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("reused startup lease: %v", err)
	}
	cfg.Control = peer
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown()
	for range 2 {
		if err := restarted.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		_, release, err := peer.Enter(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
}

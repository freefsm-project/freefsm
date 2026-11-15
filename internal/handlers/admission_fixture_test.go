package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admission uses the real Manager, whose installed instance is in public. These
// fixtures therefore own a database, rather than pretending a test search_path
// is a whole instance. The application keeps its ordinary unprivileged role.
func admissionDatabase(t *testing.T) string {
	t.Helper()
	return testutil.InstanceDatabase(t)
}

func admissionManager(t *testing.T, pool *pgxpool.Pool, control *instancecontrol.Control) *backup.Manager {
	t.Helper()
	uploads := filepath.Join(filepath.Dir(control.StateDir()), filepath.Base(control.StateDir())+"-uploads")
	if err := os.MkdirAll(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := backup.New(backup.Config{DSN: pool.Config().ConnString(), UploadDir: uploads, StateDir: control.StateDir(), Control: control, BuildKind: backup.BuildDevelopment, ResetConnections: pool.Reset})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m
}

func recoverAdmission(t *testing.T, m *backup.Manager) {
	t.Helper()
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Called while the fixture owns startup maintenance, just as main imports before
// opening ordinary admission. This is installation provenance, not an enable action.
func importFixtureEmailPolicy(t *testing.T, pool *pgxpool.Pool, control *instancecontrol.Control, manager *backup.Manager) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := emailpolicy.Import(ctx, tx, control.LegacyEmailDisabled); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	bindFixtureEmailPolicy(pool, control, manager)
}

func bindFixtureEmailPolicy(pool *pgxpool.Pool, control *instancecontrol.Control, manager *backup.Manager) {
	control.SetEmailPolicy(func(ctx context.Context) (bool, error) {
		if err := manager.RefreshConnections(); err != nil {
			return false, err
		}
		policy, err := emailpolicy.Read(ctx, pool, false)
		return policy.Enabled, err
	})
}

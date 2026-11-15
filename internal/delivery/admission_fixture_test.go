package delivery

import (
	"context"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The caller owns a whole public-schema instance, including in child workers.
// Startup imports legacy provenance once; subsequent workers read current policy.
func deliveryAdmission(t *testing.T, pool *pgxpool.Pool, state string, workerOnly ...bool) (*instancecontrol.Control, *backup.Manager) {
	t.Helper()
	c, err := instancecontrol.New(state)
	if err != nil {
		t.Fatal(err)
	}
	uploads := c.StateDir() + "-uploads"
	if err := os.MkdirAll(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := backup.New(backup.Config{DSN: pool.Config().ConnString(), StateDir: c.StateDir(), UploadDir: uploads, Control: c, BuildKind: backup.BuildDevelopment, ResetConnections: pool.Reset})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	c.SetEmailPolicy(func(ctx context.Context) (bool, error) {
		if err := m.RefreshConnections(); err != nil {
			return false, err
		}
		policy, err := emailpolicy.Read(ctx, pool, false)
		return policy.Enabled, err
	})
	// A worker joins the parent's initialized instance; it must not compete for
	// a startup maintenance reservation with another concurrently claiming worker.
	if len(workerOnly) != 0 && workerOnly[0] {
		return c, m
	}
	ctx := context.Background()
	startup, err := m.BeginStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer startup.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := emailpolicy.Import(ctx, tx, c.LegacyEmailDisabled); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := startup.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func (f *deliveryFixture) service(publicURL string) *Service {
	s := New(f.db.Pool, publicURL)
	s.SetInstanceControl(f.control)
	s.SetBeforeWork(f.manager.RefreshConnections)
	return s
}

func (f *deliveryFixture) disableEmail(t *testing.T) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE restore_email_policy SET enabled=false, revision=revision+1`); err != nil {
		t.Fatal(err)
	}
	if !f.control.EmailDisabled() {
		t.Fatal("current policy did not disable email")
	}
}

func (f *deliveryFixture) enableEmail(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	policy, err := emailpolicy.Read(ctx, f.db.Pool, false)
	if err != nil {
		t.Fatal(err)
	}
	session := uuid.NewString()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES($1,$2,'web',clock_timestamp()+interval '1 hour')`, f.admin, session); err != nil {
		t.Fatal(err)
	}
	a := saveaction.Action{UserID: f.admin, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: policy.Review(), IntentHash: "enable", Atomic: true, NoChange: true}
	if ok, err := (saveaction.Store{Pool: f.db.Pool}).AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
		t.Fatalf("admit enable: %v %v", ok, err)
	}
	if err := authorization.New(f.db.Pool).EnableEmail(saveaction.WithAction(ctx, a), authorization.Actor{UserID: f.admin, SessionHash: session, Channel: "web"}); err != nil {
		t.Fatal(err)
	}
}

package delivery

import (
	"context"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func bootstrapDeliveryAdmission(t *testing.T, f *deliveryFixture, c *instancecontrol.Control) {
	t.Helper()
	uploads := c.StateDir() + "-uploads"
	if err := os.MkdirAll(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := backup.New(backup.Config{DSN: f.db.Pool.Config().ConnString(), StateDir: c.StateDir(), UploadDir: uploads, Control: c, BuildKind: backup.BuildDevelopment, ResetConnections: f.db.Pool.Reset})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}

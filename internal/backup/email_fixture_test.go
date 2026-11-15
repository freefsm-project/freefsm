package backup

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/emailpolicy"
)

// Observe persisted policy independently of admission: closed admission must not
// mask a lost restore protection. Legacy fixtures explicitly have no policy table.
func crashEmailDisabled(t *testing.T, m *Manager) bool {
	t.Helper()
	ctx := context.Background()
	c, err := m.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var modern bool
	if err = c.QueryRow(ctx, `SELECT to_regclass('public.restore_email_policy') IS NOT NULL`).Scan(&modern); err != nil {
		t.Fatal(err)
	}
	if modern {
		p, err := emailpolicy.Read(ctx, c, false)
		if err != nil {
			t.Fatal(err)
		}
		return !p.Enabled
	}
	disabled, err := m.cfg.Control.LegacyEmailDisabled()
	if err != nil {
		t.Fatal(err)
	}
	return disabled
}

func disableCrashEmail(t *testing.T, m *Manager) {
	t.Helper()
	ctx := context.Background()
	c, err := m.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err = c.Exec(ctx, `UPDATE restore_email_policy SET enabled=false, revision=revision+1`); err != nil {
		t.Fatal(err)
	}
	if !crashEmailDisabled(t, m) {
		t.Fatal("fixture policy remained enabled")
	}
}

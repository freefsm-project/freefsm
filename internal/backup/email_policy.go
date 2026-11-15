package backup

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
)

// These reads use new engine connections under maintenance, never ordinary
// admission or an application pool from the replaced generation.
func (m *Manager) emailPolicyVersion(ctx context.Context) (int, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close(ctx)
	var modern bool
	err = c.QueryRow(ctx, `SELECT to_regclass('public.restore_email_policy_import') IS NOT NULL`).Scan(&modern)
	if err != nil {
		return 0, err
	}
	var migrations bool
	if err = c.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&migrations); err != nil {
		return 0, err
	}
	if migrations {
		var applied bool
		if err = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE name='127_restore_email_policy')`).Scan(&applied); err != nil {
			return 0, err
		}
		modern = modern || applied
	}
	if modern {
		return 1, err
	}
	return 0, err
}

func (m *Manager) currentEmailPolicy(ctx context.Context) (emailpolicy.State, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return emailpolicy.State{}, err
	}
	defer c.Close(ctx)
	return emailpolicy.Read(ctx, c, false)
}

// Package emailpolicy owns the current-generation restore mail decision.
// It never treats missing database state as permission to send.
package emailpolicy

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrUnavailable = errors.New("restore email policy unavailable")

type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type State struct {
	Epoch               string
	Enabled             bool
	Revision            int64
	LastEnableOperation *string
}

func (s State) Review() string { return s.Epoch + "/" + strconv.FormatInt(s.Revision, 10) }

// Read requires both the explicit import fact and a policy for the current
// session generation. Engine callers use their refreshed maintenance connection.
func Read(ctx context.Context, q Querier, lock bool) (State, error) {
	var s State
	query := `SELECT p.epoch::text,p.enabled,p.revision,p.last_enable_operation::text
	FROM restore_email_policy p CROSS JOIN restore_email_policy_import i
	CROSS JOIN session_instance s
	WHERE p.singleton AND i.singleton AND i.complete AND p.epoch=s.epoch`
	if lock {
		query += ` FOR UPDATE OF p`
	}
	if err := q.QueryRow(ctx, query).Scan(&s.Epoch, &s.Enabled, &s.Revision, &s.LastEnableOperation); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if id, err := uuid.Parse(s.Epoch); err != nil || id == uuid.Nil || s.Revision <= 0 {
		return State{}, ErrUnavailable
	}
	return s, nil
}

// Durable is required before protection and enablement, not just at startup.
// SET LOCAL overrides a session's asynchronous commit preference.
func Durable(ctx context.Context, tx pgx.Tx) error {
	var supported bool
	if err := tx.QueryRow(ctx, `SELECT current_setting('fsync')='on' AND current_setting('full_page_writes')='on' AND current_setting('wal_level') IN ('replica','logical')`).Scan(&supported); err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("%w: require PostgreSQL fsync=on, full_page_writes=on and wal_level=replica or logical", ErrUnavailable)
	}
	_, err := tx.Exec(ctx, `SET LOCAL synchronous_commit=on`)
	return err
}

// Import must run under exclusive startup maintenance after journal recovery.
// The callback is consulted only for a provably uncompleted import. Marker bytes
// remain untouched. Commit ambiguity is reconciled by the next invocation.
func Import(ctx context.Context, tx pgx.Tx, legacyDisabled func() (bool, error)) error {
	if err := Durable(ctx, tx); err != nil {
		return err
	}
	var complete bool
	if err := tx.QueryRow(ctx, `SELECT complete FROM restore_email_policy_import WHERE singleton FOR UPDATE`).Scan(&complete); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if complete {
		_, err := Read(ctx, tx, true)
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM restore_email_policy)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrUnavailable
	}
	disabled, err := legacyDisabled()
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO restore_email_policy(singleton,epoch,enabled,revision) SELECT true,epoch,$1,1 FROM session_instance`, !disabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	tag, err = tx.Exec(ctx, `UPDATE restore_email_policy_import SET complete=true WHERE singleton AND NOT complete`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	_, err = Read(ctx, tx, true)
	return err
}

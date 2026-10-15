package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func (s *Service) VoidTimeEntry(ctx context.Context, a Actor, id int64, reason string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		view, err := readTimeEntryAccess(ctx, tx, p, id, "time.void", true)
		if err != nil {
			return err
		}
		if view.VoidedAt != nil {
			return &ClockConflict{"time_void"}
		}
		if strings.TrimSpace(reason) == "" {
			return &ClockConflict{"reason_required"}
		}
		if err := checkTimeSave(ctx, tx, a, id); err != nil {
			return err
		}
		before, err := readTimeState(ctx, tx, id)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `UPDATE time_entries SET voided_at=$2,updated_at=$2 WHERE id=$1`, id, now); err != nil {
			return err
		}
		if err := recordTimeHistory(ctx, tx, p.UserID, id, "voided", reason, &before, now); err != nil {
			return err
		}
		assigned := false
		if view.JobID > 0 {
			assigned, err = assignedJobAccess(ctx, tx, p.UserID, view.JobID, true)
			if err != nil {
				return err
			}
		}
		scope := timeEntryGrant(p, "time.void", view.UserID == p.UserID, assigned)
		after, err := readTimeState(ctx, tx, id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "time.void", scope, "time_entry", id, map[string]any{"reason": reason, "before": before, "after": after, "accepted_at": now})
	})
}

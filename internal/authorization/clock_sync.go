package authorization

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// SyncClocks discloses the authenticated subject's active/clocked entries. Its
// continuity namespace cannot be confused with a Job snapshot. Job View is not
// required: retained own close-out survives assignment and archive changes.
func (s *Service) SyncClocks(ctx context.Context, a Actor, in SyncRequest) (SyncPage, error) {
	if in.PageSize < 1 || in.PageSize > 200 || in.Scope != "own" || a.SessionHash == "" {
		return SyncPage{}, ErrSyncResnapshot
	}
	in.Scope = "clocks:own"
	return s.syncRecords(ctx, a, in, "time.view", Own, syncClockReadModel)
}

func syncClockReadModel(ctx context.Context, tx pgx.Tx, p Principal, _ Actor, _ string) ([]syncEntry, error) {
	rows, err := tx.Query(ctx, `SELECT e.id FROM time_entries e WHERE e.company_id=$1 AND e.user_id=$2
 AND (NOT e.is_manual OR e.clock_out IS NULL OR EXISTS(SELECT 1 FROM time_entry_history h WHERE h.entry_id=e.id AND h.event='clocked_out')) ORDER BY e.id`, p.CompanyID, p.UserID)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []syncEntry{}
	for _, id := range ids {
		access, err := readTimeEntryAccess(ctx, tx, p, id, "", false)
		if err != nil {
			return nil, err
		}
		state, err := readTimeState(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		opaque, err := syncIdentity(ctx, tx, "clock", id)
		if err != nil {
			return nil, err
		}
		data := syncClockState(state)
		data["id"], data["entry_id"] = opaque, strconv.FormatInt(id, 10)
		data["capabilities"] = map[string]bool{"clock_out": state.ClockOut == nil && state.VoidedAt == nil && p.Allows("time.clock_out", Own), "gps_capture": p.Allows("time.gps.capture", Own)}
		data["elapsed_microseconds"] = nil
		if state.ClockOut != nil && state.VoidedAt == nil && state.ClockOut.After(state.ClockIn) {
			elapsed, err := elapsedMicroseconds(state.ClockIn, *state.ClockOut)
			if err != nil {
				return nil, err
			}
			data["elapsed_microseconds"] = strconv.FormatInt(int64(elapsed), 10)
		}
		history, err := readTimeEntryHistory(ctx, tx, p, id)
		if err != nil {
			return nil, err
		}
		revisions := []map[string]any{}
		for _, h := range history {
			var before any
			if h.Before != nil {
				before = syncClockState(*h.Before)
			}
			revisions = append(revisions, map[string]any{"actor_id": strconv.FormatInt(h.ActorID, 10), "actor": h.Actor, "accepted_at": h.AcceptedAt, "event": h.Event, "reason": h.Reason, "before": before, "after": syncClockState(h.After)})
		}
		data["history"] = revisions
		if access.CanGPS {
			facts, err := readClockGPS(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			gps := []map[string]any{}
			for _, f := range facts {
				v := map[string]any{"event": f.Event, "latitude": f.Latitude, "longitude": f.Longitude, "captured_at": f.CapturedAt}
				if f.Accuracy != nil {
					v["accuracy"] = *f.Accuracy
				}
				gps = append(gps, v)
			}
			data["gps"] = gps
		}
		// A previously delivered GPS facet must be purged when its Assigned
		// scope disappears, even though the subject retains own Time access.
		anchor := strconv.FormatInt(p.UserID, 10) + ":gps=" + strconv.FormatBool(access.CanGPS)
		out = append(out, syncEntry{InstalledID: id, ID: opaque, Anchor: anchor, Data: data})
	}
	return out, nil
}

func syncClockState(s TimeState) map[string]any {
	lifecycle := "active"
	if s.ClockOut != nil {
		lifecycle = "closed"
	}
	if s.VoidedAt != nil {
		lifecycle = "void"
	}
	v := map[string]any{"subject_id": strconv.FormatInt(s.UserID, 10), "clock_in": s.ClockIn, "clock_out": s.ClockOut, "voided_at": s.VoidedAt, "state": lifecycle, "notes": s.Notes}
	if s.JobID != nil {
		v["job"] = map[string]any{"id": strconv.FormatInt(*s.JobID, 10), "title": s.JobTitle}
	}
	return v
}

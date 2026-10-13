package authorization

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ClockConflict struct{ Code string }

func (e *ClockConflict) Error() string { return e.Code }

type ClockCommand struct {
	EntryID     int64      `json:"entry_id,omitempty"`
	JobID       int64      `json:"job_id,omitempty"`
	Notes       string     `json:"notes,omitempty"`
	GPS         *ClockGPS  `json:"gps,omitempty"`
	OperationID string     `json:"operation_id,omitempty"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
	LeaseID     string     `json:"lease_id,omitempty"`
}

type ClockGPS struct {
	Latitude   *float64  `json:"latitude"`
	Longitude  *float64  `json:"longitude"`
	CapturedAt time.Time `json:"captured_at"`
	Accuracy   *float64  `json:"accuracy,omitempty"`
}

type ClockEntry struct {
	ID       int64      `json:"id"`
	UserID   int64      `json:"user_id"`
	JobID    *int64     `json:"job_id,omitempty"`
	ClockIn  time.Time  `json:"clock_in"`
	ClockOut *time.Time `json:"clock_out"`
}

// Clock accepts only the authenticated subject. The shared authority fence
// serializes assignment, archive, grant and time writers through commit.
func (s *Service) Clock(ctx context.Context, a Actor, out bool, in ClockCommand) (ClockEntry, error) {
	if a.Channel == "api" {
		return ClockEntry{}, ErrForbidden
	}
	var result ClockEntry
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var err error
		result, err = applyClock(ctx, tx, p, a, out, in)
		return err
	})
	return result, err
}

func applyClock(ctx context.Context, tx pgx.Tx, p Principal, a Actor, out bool, in ClockCommand) (ClockEntry, error) {
	var result ClockEntry
	err := func() error {
		if out && in.Notes != "" || out && in.EntryID <= 0 || !out && in.EntryID != 0 {
			return &ClockConflict{"invalid_clock"}
		}
		permission := "time.clock_in"
		if out {
			permission = "time.clock_out"
		}
		if !out && in.JobID > 0 {
			if _, err := parentAccess(ctx, tx, p, "job", in.JobID, "", true); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM jobs WHERE id=$1 AND company_id=$2`, in.JobID, p.CompanyID).Scan(&active); err != nil {
				return err
			}
			if !active {
				return ErrForbidden
			}
		}
		if !p.Allows(permission, Own) {
			return ErrForbidden
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		occurrence := now
		if in.OccurredAt != nil {
			if in.OccurredAt.Nanosecond()%1000 != 0 {
				return &ClockConflict{"invalid_occurrence"}
			}
			if a.Channel != "api" || in.LeaseID == "" || in.OccurredAt.After(now) {
				return &ClockConflict{"invalid_occurrence"}
			}
			var issued, expires time.Time
			leaseID, err := uuid.Parse(in.LeaseID)
			if err != nil || leaseID == uuid.Nil {
				return &ClockConflict{"invalid_lease"}
			}
			if err := tx.QueryRow(ctx, `SELECT issued_at,expires_at FROM time_clock_leases WHERE id=$1 AND user_id=$2 AND client_key=$3`, leaseID.String(), p.UserID, a.Channel+":"+a.SessionHash).Scan(&issued, &expires); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return &ClockConflict{"invalid_lease"}
				}
				return err
			}
			if in.OccurredAt.Before(issued) || in.OccurredAt.After(expires) {
				return &ClockConflict{"invalid_lease"}
			}
			if in.GPS != nil && (in.GPS.CapturedAt.Before(issued) || in.GPS.CapturedAt.After(expires)) {
				return &ClockConflict{"invalid_gps"}
			}
			occurrence = in.OccurredAt.UTC()
		} else if in.LeaseID != "" {
			return &ClockConflict{"invalid_occurrence"}
		}
		if in.GPS != nil {
			if !p.Allows("time.gps.capture", Own) {
				return ErrForbidden
			}
			g := in.GPS
			if g.Latitude == nil || g.Longitude == nil || math.IsNaN(*g.Latitude) || math.IsNaN(*g.Longitude) || *g.Latitude < -90 || *g.Latitude > 90 || *g.Longitude < -180 || *g.Longitude > 180 || g.CapturedAt.IsZero() || g.CapturedAt.After(now) || g.CapturedAt.Nanosecond()%1000 != 0 || g.Accuracy != nil && (math.IsNaN(*g.Accuracy) || math.IsInf(*g.Accuracy, 0) || *g.Accuracy < 0) {
				return &ClockConflict{"invalid_gps"}
			}
		}
		if out {
			err := tx.QueryRow(ctx, `SELECT id,user_id,job_id,clock_in,clock_out FROM time_entries WHERE id=$1 AND user_id=$2 AND company_id=$3 FOR UPDATE`, in.EntryID, p.UserID, p.CompanyID).Scan(&result.ID, &result.UserID, &result.JobID, &result.ClockIn, &result.ClockOut)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if in.JobID != 0 && (result.JobID == nil || *result.JobID != in.JobID) {
				return ErrNotFound
			}
			if result.ClockOut != nil {
				if in.OccurredAt != nil && result.ClockOut.Equal(occurrence) {
					// A new operation cannot attach or replace capture evidence on
					// an already accepted out. Original-operation replay is handled
					// by its receipt; explicit location-free retry may be a no-op.
					if in.GPS != nil {
						return &ClockConflict{"gps_immutable"}
					}
					return nil
				}
				return &ClockConflict{"time_different_end"}
			}
			if !occurrence.After(result.ClockIn) {
				return &ClockConflict{"time_order"}
			}
			if err := checkTimeInterval(ctx, tx, p.UserID, result.ID, result.ClockIn, &occurrence); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE time_entries SET clock_out=$2,updated_at=$3 WHERE id=$1`, result.ID, occurrence, now); err != nil {
				return err
			}
			result.ClockOut = &occurrence
		} else {
			if in.JobID < 0 {
				return &ClockConflict{"invalid_job"}
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM time_entries WHERE user_id=$1 AND clock_out IS NULL)`, p.UserID).Scan(&active); err != nil {
				return err
			}
			if active {
				return &ClockConflict{"time_active"}
			}
			if err := checkTimeInterval(ctx, tx, p.UserID, 0, occurrence, nil); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,job_id,is_manual,clock_in,notes,created_at,updated_at) VALUES($1,$2,nullif($3,0),false,$4,$5,$6,$6) RETURNING id,user_id,job_id,clock_in,clock_out`, p.CompanyID, p.UserID, in.JobID, occurrence, in.Notes, now).Scan(&result.ID, &result.UserID, &result.JobID, &result.ClockIn, &result.ClockOut); err != nil {
				return err
			}
		}
		if in.GPS != nil {
			event := "in"
			if out {
				event = "out"
			}
			g := in.GPS
			if _, err := tx.Exec(ctx, `INSERT INTO time_clock_gps(entry_id,event,latitude,longitude,captured_at,accuracy,accepted_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, result.ID, event, g.Latitude, g.Longitude, g.CapturedAt, g.Accuracy, now); err != nil {
				return err
			}
			if err := audit(ctx, tx, a, p, "time.gps.capture", Own, "time_entry", result.ID, map[string]any{"event": event, "operation_id": in.OperationID, "captured_at": g.CapturedAt, "accepted_at": now}); err != nil {
				return err
			}
		}
		action := "clocked_in"
		if out {
			action = "clocked_out"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at) VALUES($1,$2,$3,'time_entry',$4,jsonb_build_object('time_entry_id',$4::bigint,'clock_in',$5::timestamptz,'clock_out',$6::timestamptz),$7)`, p.CompanyID, p.UserID, action, result.ID, result.ClockIn, result.ClockOut, now); err != nil {
			return err
		}
		if result.JobID != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at) VALUES($1,$2,$3,'job',$4,jsonb_build_object('time_entry_id',$5::bigint),$6)`, p.CompanyID, p.UserID, action, *result.JobID, result.ID, now); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, permission, Own, "time_entry", result.ID, map[string]any{"clock_in": result.ClockIn, "clock_out": result.ClockOut, "operation_id": in.OperationID, "occurred_at": occurrence, "accepted_at": now})
	}()
	return result, err
}

// Half-open intervals permit exact adjacency without rounding or repair.
func checkTimeInterval(ctx context.Context, tx pgx.Tx, subject, id int64, start time.Time, end *time.Time) error {
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM time_entries WHERE user_id=$1 AND id<>$2 AND ($4::timestamptz IS NULL OR clock_in<$4) AND (clock_out IS NULL OR clock_out>$3))`, subject, id, start, end).Scan(&overlap); err != nil {
		return err
	}
	if overlap {
		return &ClockConflict{"time_overlap"}
	}
	return nil
}

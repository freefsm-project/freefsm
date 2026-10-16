package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"time"

	"github.com/jackc/pgx/v5"
)

// TimeState contains time facts only. Clock GPS retains its separate evidence
// and authorization boundary and is never copied to corrected instants.
type TimeState struct {
	JobTitle string     `json:"job_title"`
	VoidedAt *time.Time `json:"voided_at"`
	UserID   int64      `json:"user_id"`
	ClockIn  time.Time  `json:"clock_in"`
	ClockOut *time.Time `json:"clock_out"`
	JobID    *int64     `json:"job_id"`
	Notes    string     `json:"notes"`
}

type TimeRevision struct {
	ActorID       int64
	Actor         string
	AcceptedAt    time.Time
	Event, Reason string
	Before        *TimeState
	After         TimeState
}

// Admission is separate from the effect: even a terminally rejected browser
// intent must not be rebound to a clock or another command family. The existing
// web journal supplies the receipt; the common registry supplies UUID ownership.
func (s *Service) AdmitTimeEntrySave(ctx context.Context, a Actor, operation, method, path, digest string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, _ Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		return admitOperation(ctx, tx, a, operation, "time.entry", map[string]string{"method": method, "path": path, "intent_hash": digest})
	})
}

func checkTimeSave(ctx context.Context, tx pgx.Tx, a Actor, entryID int64) error {
	operation := saveaction.SubmittedOperationID(ctx)
	if operation == "" {
		if a.SessionHash != "" {
			return saveaction.ErrRevisionRequired
		}
		return nil // trusted in-process caller; HTTP always has a session binding
	}
	if entryID > 0 {
		if err := saveaction.RequireRevision(ctx, tx, a.UserID, fmt.Sprintf("/time-entries/%d", entryID)); err != nil {
			return err
		}
	}
	var matches bool
	if err := tx.QueryRow(ctx, `SELECT user_id=$2 AND client_key='web' AND operation_type='time.entry' FROM operation_admissions WHERE operation_id=$1`, operation, a.UserID).Scan(&matches); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if !matches {
		return ErrConflict
	}
	return nil
}

func readTimeState(ctx context.Context, q queryer, id int64) (TimeState, error) {
	var v TimeState
	err := q.QueryRow(ctx, `SELECT user_id,clock_in,clock_out,job_id,notes,voided_at,job_title_snapshot FROM time_entries WHERE id=$1`, id).Scan(&v.UserID, &v.ClockIn, &v.ClockOut, &v.JobID, &v.Notes, &v.VoidedAt, &v.JobTitle)
	return v, err
}

func recordTimeHistory(ctx context.Context, tx pgx.Tx, actor, id int64, event, reason string, before *TimeState, accepted time.Time) error {
	after, err := readTimeState(ctx, tx, id)
	if err != nil {
		return err
	}
	var old any
	if before != nil {
		data, e := json.Marshal(before)
		if e != nil {
			return e
		}
		old = data
	}
	data, err := json.Marshal(after)
	if err != nil {
		return err
	}
	var historyID int64
	err = tx.QueryRow(ctx, `INSERT INTO time_entry_history(entry_id,actor_id,actor_name,accepted_at,event,reason,before_state,after_state) VALUES($1,$2,(SELECT name FROM users WHERE id=$2),$3,$4,$5,$6,$7) RETURNING id`, id, actor, accepted, event, reason, old, data).Scan(&historyID)
	if err != nil {
		return err
	}
	if event == "corrected" || event == "voided" || event == "manual_created" {
		action := event
		if event == "manual_created" {
			action = "created"
		}
		_, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key)
 SELECT e.company_id,$2,$3,'time_entry',e.id,jsonb_build_object('before',$4::jsonb,'after',$5::jsonb || jsonb_build_object('reason',$6::text),'actor_name',h.actor_name,'channel','web','source_occurred_at',$7::timestamptz),$7,'time-entry-history:'||h.id
 FROM time_entries e JOIN time_entry_history h ON h.entry_id=e.id WHERE e.id=$1 AND h.id=$8`, id, actor, action, old, data, reason, accepted, historyID)
	}
	return err
}

func (s *Service) TimeEntryHistory(ctx context.Context, a Actor, id int64) ([]TimeRevision, error) {
	var result []TimeRevision
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		var err error
		result, err = readTimeEntryHistory(ctx, tx, p, id)
		return err
	})
	return result, err
}

func readTimeEntryHistory(ctx context.Context, tx queryer, p Principal, id int64) ([]TimeRevision, error) {
	var result []TimeRevision
	if _, err := readTimeEntryAccess(ctx, tx, p, id, "", false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT coalesce(h.actor_id,0),coalesce(h.actor_name,'Unknown historical actor'),h.accepted_at,h.event,h.reason,h.before_state,h.after_state FROM time_entry_history h WHERE h.entry_id=$1 ORDER BY h.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v TimeRevision
		var before, after []byte
		if err := rows.Scan(&v.ActorID, &v.Actor, &v.AcceptedAt, &v.Event, &v.Reason, &before, &after); err != nil {
			return nil, err
		}
		if len(before) > 0 {
			if err := json.Unmarshal(before, &v.Before); err != nil {
				return nil, err
			}
		}
		if err := json.Unmarshal(after, &v.After); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range result {
		for _, state := range []*TimeState{result[i].Before, &result[i].After} {
			if state == nil || state.UserID == p.UserID || state.JobID == nil {
				continue
			}
			if _, err := parentAccess(ctx, tx, p, "job", *state.JobID, "", false); err != nil {
				if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
					return nil, err
				}
				state.JobTitle = ""
			}
		}
	}
	return result, nil
}

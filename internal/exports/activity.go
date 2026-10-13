package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

type Activity struct{ ExportScope authorization.Scope }

func (a Activity) Kind() string {
	if a.Scope() == authorization.All {
		return "operational-activity-all-v1"
	}
	return "operational-activity-v1"
}
func (Activity) Permission() string { return "activity.export" }
func (a Activity) Scope() authorization.Scope {
	if a.ExportScope == authorization.All {
		return authorization.All
	}
	return authorization.Assigned
}
func (Activity) Columns() []string {
	return []string{"activity_id", "accepted_at", "occurred_at", "actor_id", "actor", "record_type", "record_id", "record_name", "action", "before_after"}
}
func (a Activity) Validate(raw json.RawMessage) error {
	if !json.Valid(raw) {
		return ErrConflict
	}
	var f authorization.ActivityFilter
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return ErrConflict
	}
	if f.ExportScope != a.Scope() {
		return ErrConflict
	}
	return authorization.ValidateActivityFilter(f)
}
func (a Activity) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := a.Validate(raw); err != nil {
		return err
	}
	if !p.Allows(a.Permission(), a.Scope()) {
		return authorization.ErrForbidden
	}
	return nil
}
func (Activity) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var f authorization.ActivityFilter
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	return authorization.CheckActivitySelection(ctx, tx, p, f)
}
func (Activity) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	var f authorization.ActivityFilter
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	return authorization.WalkActivity(ctx, tx, p, f, true, func(e authorization.ActivityRecord) (bool, error) {
		ref, err := json.Marshal(e)
		if err != nil {
			return false, err
		}
		facts, err := json.Marshal(e.Facts)
		if err != nil {
			return false, err
		}
		occurred := ""
		if e.Occurred != nil {
			occurred = e.Occurred.UTC().Format(time.RFC3339Nano)
		}
		err = emit(Record{Reference: ref, Values: []string{fmt.Sprint(e.ID), e.Accepted.UTC().Format(time.RFC3339Nano), occurred, fmt.Sprint(e.ActorID), e.Actor, e.Type, fmt.Sprint(e.RecordID), e.Name, e.Action, string(facts)}})
		return err == nil, err
	})
}
func (Activity) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var e authorization.ActivityRecord
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	return authorization.RecheckActivity(ctx, tx, p, e)
}

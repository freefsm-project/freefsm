package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobFieldSnapshot is a closed field facet, materialized in the same authorized
// snapshot as the Job. Definition changes therefore participate in state deltas
// without invalidating independent value revision tokens.
type JobFieldSnapshot struct {
	FieldState
	Definitions []JobFieldDefinition `json:"definitions"`
	CanEdit     bool                 `json:"can_edit"`
	History     []JobFieldChange     `json:"history"`
}

type JobFieldDefinition struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Precision      *int     `json:"precision"`
	Required       bool     `json:"required"`
	Retired        bool     `json:"retired"`
	Options        []string `json:"options"`
	RetiredOptions []string `json:"retired_options"`
	HelpText       string   `json:"help_text"`
}

// Only recorded operational field facts are exposed, never sealed Role/session
// evidence or guessed historical revisions/attribution.
type JobFieldChange struct {
	ID         int64           `json:"id"`
	ActorID    *int64          `json:"actor_id"`
	AcceptedAt time.Time       `json:"accepted_at"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
}

func jobFieldSnapshot(ctx context.Context, tx pgx.Tx, p Principal, id int64) (JobFieldSnapshot, error) {
	out := JobFieldSnapshot{Definitions: []JobFieldDefinition{}, History: []JobFieldChange{}}
	if _, err := parentAccess(ctx, tx, p, "job", id, "", false); err != nil {
		return out, err
	}
	_, err := parentAccess(ctx, tx, p, "job", id, "job.fields.edit", false)
	out.CanEdit = err == nil
	if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
		return out, err
	}
	var values string
	if err := tx.QueryRow(ctx, `SELECT custom_fields::text FROM jobs WHERE id=$1`, id).Scan(&values); err != nil {
		return out, err
	}
	out.FieldState, err = fieldState(ctx, tx, p.CompanyID, id, values)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT id,name,field_type,precision,required,retired,options::jsonb,retired_options,help_text FROM custom_field_definitions WHERE object_type='job' AND custom_field_applies_to_company(company_id,$1) ORDER BY sort_order,id`, p.CompanyID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d JobFieldDefinition
		if err := rows.Scan(&d.ID, &d.Name, &d.Type, &d.Precision, &d.Required, &d.Retired, &d.Options, &d.RetiredOptions, &d.HelpText); err != nil {
			rows.Close()
			return out, err
		}
		out.Definitions = append(out.Definitions, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT id,actor_id,recorded_at,(detail->>'before')::jsonb,(detail->>'after')::jsonb FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND action='job.fields.edit' AND detail ? 'before' AND detail ? 'after' ORDER BY recorded_at,id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h JobFieldChange
		if err := rows.Scan(&h.ID, &h.ActorID, &h.AcceptedAt, &h.Before, &h.After); err != nil {
			return out, err
		}
		out.History = append(out.History, h)
	}
	return out, rows.Err()
}

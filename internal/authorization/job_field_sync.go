package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// syncJobFields enriches the existing coherent, authorized Job projection. It
// reads the owning workflow/checklist facts in the same transaction as context;
// definitions and revisions cannot be assembled from different observations.
func syncJobFields(ctx context.Context, tx pgx.Tx, p Principal, e *syncEntry) error {
	var revision string
	var statusID *int64
	if err := tx.QueryRow(ctx, `SELECT status_revision::text,status_id FROM jobs WHERE id=$1`, e.InstalledID).Scan(&revision, &statusID); err != nil {
		return err
	}
	e.Data["status_revision"] = revision
	status := e.Data["status"].(map[string]any)
	status["id"], status["meaning_revision"] = nil, nil
	if statusID != nil {
		status["id"] = strconv.FormatInt(*statusID, 10)
	}
	definitions := []map[string]any{}
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.name,s.color,s.category_key,s.category_order,s.meaning_revision::text
	 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
	 WHERE s.company_id=$1 AND w.company_id=$1 AND w.object_type='job'
	 ORDER BY CASE s.category_key WHEN 'job:new' THEN 1 WHEN 'job:travel_time' THEN 2 WHEN 'job:in_progress' THEN 3 WHEN 'job:pending' THEN 4 WHEN 'job:completed' THEN 5 WHEN 'job:canceled' THEN 6 END,s.category_order,s.id`, p.CompanyID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, color, category, meaning string
		var order int
		if err := rows.Scan(&id, &name, &color, &category, &order, &meaning); err != nil {
			rows.Close()
			return err
		}
		definitions = append(definitions, map[string]any{"id": id, "name": name, "color": color, "category": category, "order": order, "meaning_revision": meaning})
		if status["id"] == id {
			status["meaning_revision"] = meaning
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	e.Data["status_definitions"] = definitions
	// Only recorded attribution for this exact revision is reported. Installed
	// baselines and configuration replacements do not acquire invented actors.
	var attribution []byte
	err = tx.QueryRow(ctx, `SELECT jsonb_build_object('actor_id',actor_id::text,'accepted_at',created_at,'occurred_at',metadata->'occurred_at') FROM activity_logs WHERE company_id=$1 AND object_type='job' AND object_id=$2 AND action='status_transitioned' AND metadata->>'revision'=$3 ORDER BY id DESC LIMIT 1`, p.CompanyID, e.InstalledID, revision).Scan(&attribution)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	e.Data["status_attribution"] = nil
	if err == nil {
		e.Data["status_attribution"] = json.RawMessage(attribution)
	}
	checklist, err := subtaskChecklist(ctx, tx, e.InstalledID)
	if err != nil {
		return err
	}
	e.Data["subtasks"], e.Data["subtask_history"], e.Data["subtask_review_required"] = checklist.Entries, checklist.History, checklist.ReviewRequired
	var assigned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$1 AND user_id=$2)`, e.InstalledID, p.UserID).Scan(&assigned); err != nil {
		return err
	}
	allowed := func(permission string) bool {
		return p.Allows(permission, All) || assigned && p.Allows(permission, Assigned)
	}
	e.Data["field_capabilities"] = map[string]bool{"status": allowed("job.status"), "subtask_complete": !checklist.ReviewRequired && allowed("job.subtask.complete"), "subtask_reopen": !checklist.ReviewRequired && allowed("job.subtask.reopen")}
	// The neutral canonicalizer never rewrites recorded field identities.
	e.Data, err = syncJSONObject(e.Data)
	return err
}

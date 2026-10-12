package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// WorkflowPreflight inventories raw definitions and every record, without
// filtering archive/conversion visibility or guessing a meaning from a label.
type WorkflowPreflight struct {
	Snapshot string            `json:"snapshot"`
	Evidence json.RawMessage   `json:"evidence"`
	Mappings map[string]string `json:"mappings"`
}

func workflowInventory(ctx context.Context, tx pgx.Tx) (WorkflowPreflight, error) {
	var out WorkflowPreflight
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
	 'statuses',coalesce((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM statuses s),'[]'),
	 'workflows',coalesce((SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM status_workflows w),'[]'),
	 'references',coalesce((SELECT jsonb_agg(r ORDER BY r->>'type',r->>'id') FROM (
	 SELECT jsonb_build_object('type','job','id',id,'status_id',status_id,'archived_at',deleted_at) r FROM jobs
	 UNION ALL SELECT jsonb_build_object('type','project','id',id,'status_id',status_id,'archived_at',deleted_at) FROM projects
	 UNION ALL SELECT jsonb_build_object('type','estimate','id',id,'status_id',status_id,'archived_at',deleted_at,'hidden_at',to_jsonb(estimates)->'conversion_hidden_at') FROM estimates
	 UNION ALL SELECT jsonb_build_object('type','invoice','id',id,'status_id',status_id,'archived_at',deleted_at,'hidden_at',to_jsonb(invoices)->'conversion_hidden_at') FROM invoices) refs),'[]'))`).Scan(&out.Evidence)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256(out.Evidence)
	out.Snapshot = hex.EncodeToString(hash[:])
	out.Mappings = map[string]string{}
	return out, nil
}

func (db *DB) WorkflowPreflight(ctx context.Context) (WorkflowPreflight, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return WorkflowPreflight{}, err
	}
	defer tx.Rollback(ctx)
	return workflowInventory(ctx, tx)
}

func preflightWorkflow(ctx context.Context, tx pgx.Tx, fresh bool, reviewed *WorkflowPreflight) error {
	if _, err := tx.Exec(ctx, `LOCK TABLE statuses,status_workflows,jobs,projects,estimates,invoices IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM statuses`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	inventory, err := workflowInventory(ctx, tx)
	if err != nil {
		return err
	}
	if fresh {
		// These are this binary's newly inserted seed rows, never installed data.
		_, err = tx.Exec(ctx, `SELECT set_config('freefsm.workflow_review',coalesce((SELECT jsonb_object_agg(s.id::text,w.object_type||':'||CASE lower(s.name) WHEN 'scheduled' THEN 'pending' WHEN 'on hold' THEN 'pending' WHEN 'opportunity' THEN 'new' WHEN 'planning' THEN 'in_progress' WHEN 'declined' THEN 'rejected' WHEN 'paid' THEN 'sent' WHEN 'partially paid' THEN 'sent' ELSE replace(lower(s.name),' ','_') END)::text FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id),'{}'),true)`)
		return err
	}
	if reviewed != nil {
		inventory = *reviewed
	}
	var review WorkflowPreflight
	if reviewed != nil {
		review = *reviewed
	} else {
		var err error
		review, err = loadWorkflowReview(inventory, count)
		if err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(review.Mappings)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.workflow_review',$1,true)`, string(encoded)); err != nil {
		return err
	}
	// Preserve pre-upgrade evidence, including identities an earlier legacy
	// settlement migration may consolidate. No transition history is invented.
	if _, err = tx.Exec(ctx, `CREATE TABLE workflow_migration_evidence(snapshot text PRIMARY KEY,evidence jsonb NOT NULL,mappings jsonb NOT NULL,reviewed_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workflow_migration_evidence(snapshot,evidence,mappings) VALUES($1,$2,$3)`, inventory.Snapshot, inventory.Evidence, encoded)
	return err
}

func loadWorkflowReview(inventory WorkflowPreflight, count int) (WorkflowPreflight, error) {
	path := os.Getenv("FREEFSM_WORKFLOW_MAPPING_FILE")
	if path == "" {
		return inventory, fmt.Errorf("workflow review required before migration 044; export WorkflowPreflight and supply reviewed FREEFSM_WORKFLOW_MAPPING_FILE (snapshot %s)", inventory.Snapshot)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return inventory, fmt.Errorf("read workflow review: %w", err)
	}
	var review WorkflowPreflight
	if err = json.Unmarshal(data, &review); err != nil {
		return inventory, fmt.Errorf("invalid workflow review: %w", err)
	}
	if review.Snapshot != inventory.Snapshot || len(review.Mappings) != count {
		return inventory, fmt.Errorf("workflow review is stale or incomplete")
	}
	var evidence struct {
		Statuses []struct {
			ID         int64 `json:"id"`
			WorkflowID int64 `json:"workflow_id"`
		} `json:"statuses"`
		Workflows []struct {
			ID   int64  `json:"id"`
			Type string `json:"object_type"`
		} `json:"workflows"`
	}
	if err = json.Unmarshal(inventory.Evidence, &evidence); err != nil {
		return inventory, err
	}
	types := map[int64]string{}
	for _, w := range evidence.Workflows {
		types[w.ID] = w.Type
	}
	allowed := map[string]bool{}
	for typ, keys := range map[string][]string{"job": {"new", "travel_time", "in_progress", "pending", "completed", "canceled"}, "project": {"new", "in_progress", "pending", "completed", "canceled"}, "estimate": {"draft", "estimate", "sent", "accepted", "rejected", "completed"}, "invoice": {"draft", "invoiced", "sent", "void"}} {
		for _, key := range keys {
			allowed[typ+":"+key] = true
		}
	}
	for _, status := range evidence.Statuses {
		key := review.Mappings[fmt.Sprint(status.ID)]
		if !allowed[key] || !strings.HasPrefix(key, types[status.WorkflowID]+":") {
			return inventory, fmt.Errorf("workflow review has an invalid category for status %d", status.ID)
		}
	}
	inventory.Mappings = review.Mappings
	return inventory, nil
}

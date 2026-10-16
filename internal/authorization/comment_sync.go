package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

// Synchronization reuses the ordinary collaboration projection in the snapshot's
// authority transaction. Documents contribute collaboration only, never broader
// commercial data. Opaque aliases use the existing installed-identity registry.
func syncComments(ctx context.Context, tx pgx.Tx, p Principal, a Actor, job *syncEntry) error {
	anchors := []any{job.Anchor}
	project := func(ref objectref.Ref) (map[string]any, error) {
		comments, err := readComments(ctx, tx, p, a, ref)
		if err != nil {
			return nil, err
		}
		for _, c := range comments.Entries {
			if c.RemovedAt != nil {
				anchors = append(anchors, fmt.Sprintf("removed-comment:%d", c.ID))
			}
		}
		return syncDTO(ctx, tx, "comment_collection", comments)
	}
	comments, err := project(objectref.New(objectref.TypeJob, job.InstalledID))
	if err != nil {
		return err
	}
	job.Data["comments"] = comments
	rows, err := tx.Query(ctx, `SELECT 'estimate',id FROM estimates WHERE company_id=$1 AND job_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL
	 UNION ALL SELECT 'invoice',id FROM invoices WHERE company_id=$1 AND job_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL ORDER BY 1,2`, p.CompanyID, job.InstalledID)
	if err != nil {
		return err
	}
	var parents []objectref.Ref
	for rows.Next() {
		var ref objectref.Ref
		if err := rows.Scan(&ref.Type, &ref.ID); err != nil {
			rows.Close()
			return err
		}
		parents = append(parents, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	documents := []map[string]any{}
	for _, ref := range parents {
		comments, err := project(ref)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		id, err := syncIdentity(ctx, tx, string(ref.Type), ref.ID)
		if err != nil {
			return err
		}
		anchors = append(anchors, string(ref.Type)+":"+id)
		documents = append(documents, map[string]any{"parent_type": string(ref.Type), "id": id, "comments": comments})
	}
	job.Data["document_comments"] = documents
	// A stale materialized page must not disclose text removed since it was read,
	// or collaboration from a document that lost its original relationship.
	encoded, err := json.Marshal(anchors)
	job.Anchor = string(encoded)
	return err
}

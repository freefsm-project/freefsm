package authorization

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

var ErrCommentParentChanged = errors.New("comment parent changed; review fresh intent")

// The caller has checked and locked the exact active parent with commentParent.
// Eligibility excludes ordinary field edits, while retaining lifecycle ABA and
// original Job/own-assignment identity. This token is not mutation authority.
func commentEligibility(ctx context.Context, tx pgx.Tx, p Principal, a Actor, ref objectref.Ref) (string, error) {
	jobID := ref.ID
	switch ref.Type {
	case objectref.TypeJob:
	case objectref.TypeEstimate, objectref.TypeInvoice:
		table := "estimates"
		if ref.Type == objectref.TypeInvoice {
			table = "invoices"
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+table+` WHERE id=$1 AND company_id=$2`, ref.ID, p.CompanyID).Scan(&jobID); err != nil {
			return "", err
		}
	default:
		return "", ErrNotFound
	}
	var assignment int64
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT id FROM job_assignments WHERE job_id=j.id AND user_id=$2),0) FROM jobs j WHERE j.id=$1 AND j.company_id=$3 AND j.deleted_at IS NULL FOR SHARE OF j`, jobID, a.UserID, p.CompanyID).Scan(&assignment); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	// Like sync aliases, this creates only an authorized current protocol baseline.
	// It does not reconstruct a prior transition or change business rows/history.
	if _, err := tx.Exec(ctx, `INSERT INTO comment_parent_eligibility(object_type,object_id) VALUES($1,$2),('job',$3) ON CONFLICT DO NOTHING`, string(ref.Type), ref.ID, jobID); err != nil {
		return "", err
	}
	var parentRevision, jobRevision string
	if err := tx.QueryRow(ctx, `SELECT p.revision::text,j.revision::text FROM comment_parent_eligibility p JOIN comment_parent_eligibility j ON j.object_type='job' AND j.object_id=$3 WHERE p.object_type=$1 AND p.object_id=$2`, string(ref.Type), ref.ID, jobID).Scan(&parentRevision, &jobRevision); err != nil {
		return "", err
	}
	state := fmt.Sprintf("comment-parent-v1:%d:%d:%s:%d:%s:%d:%s:%d", p.CompanyID, a.UserID, ref.Type, ref.ID, parentRevision, jobID, jobRevision, assignment)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(state))), nil
}

func validateCommentEligibility(ctx context.Context, tx pgx.Tx, p Principal, a Actor, ref objectref.Ref, in CommentCommand) error {
	if a.Channel != "api" && !in.requireParentRevision && in.ParentRevision == "" {
		return nil
	}
	revision, err := commentEligibility(ctx, tx, p, a, ref)
	if err != nil {
		return err
	}
	if in.ParentRevision == "" || in.ParentRevision != revision {
		return ErrCommentParentChanged
	}
	return nil
}

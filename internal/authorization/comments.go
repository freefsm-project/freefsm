package authorization

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCommentInvalid = errors.New("comment must contain 1 to 10000 plain-text characters")

// Comment is the ordinary, parent-bounded projection, never a retained-text record.
type Comment struct {
	ID        int64      `json:"id"`
	AuthorID  int64      `json:"author_id"`
	Author    string     `json:"author"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	CanRemove bool       `json:"can_remove"`
	ReplyTo   *int64     `json:"reply_to,omitempty"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
	RemovedBy *int64     `json:"removed_by,omitempty"`
	Remover   string     `json:"remover,omitempty"`
	CanReply  bool       `json:"can_reply"`
}
type Comments struct {
	Entries             []Comment `json:"comments"`
	CanAdd              bool      `json:"can_add"`
	EligibilityRevision string    `json:"eligibility_revision,omitempty"`
}

// Lock the exact parent before reading mutable lifecycle and assignment facts.
// The authorization transaction also serializes grant/session changes. No parent
// revision is required: unrelated edits do not invalidate create-once intent.
func commentParent(ctx context.Context, tx pgx.Tx, p Principal, a Actor, ref objectref.Ref, write bool) (Scope, error) {
	table := ""
	switch ref.Type {
	case objectref.TypeCustomer:
		table = "customers"
	case objectref.TypeProject:
		table = "projects"
	case objectref.TypeJob:
		table = "jobs"
	case objectref.TypeAsset:
		table = "assets"
	case objectref.TypeEstimate:
		table = "estimates"
	case objectref.TypeInvoice:
		table = "invoices"
	default:
		return "", ErrNotFound
	}
	var active bool
	var jobID int64
	jobExpr := "0"
	if ref.Type == objectref.TypeJob {
		jobExpr = "id"
	}
	if ref.Type == objectref.TypeInvoice || ref.Type == objectref.TypeEstimate {
		jobExpr = "coalesce(job_id,0)"
	}
	hidden := ""
	if ref.Type == objectref.TypeInvoice || ref.Type == objectref.TypeEstimate {
		hidden = " AND conversion_hidden_at IS NULL"
	}
	err := tx.QueryRow(ctx, fmt.Sprintf("SELECT deleted_at IS NULL,%s FROM %s WHERE id=$1 AND company_id=$2%s FOR UPDATE", jobExpr, table, hidden), ref.ID, p.CompanyID).Scan(&active, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	view := string(ref.Type) + ".view"
	scope := All
	if jobID != 0 {
		// Assignment deletion and Job archive must not commit between validation
		// and acceptance. Lock the relationship and Job, even for broad View with
		// a narrower collaboration grant.
		var assignedID int64
		err := tx.QueryRow(ctx, `SELECT j.id FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=$1 AND a.user_id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL FOR SHARE OF j,a`, jobID, a.UserID, p.CompanyID).Scan(&assignedID)
		if err == nil {
			scope = Assigned
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if !p.Allows(view, scope) {
		return "", ErrNotFound
	}
	if !active && !p.Allows(string(ref.Type)+".archived.view", All) {
		return "", ErrNotFound
	}
	if write && !active && a.Channel != "api" {
		return "", ErrConflict
	}
	// The field API never expands into Customer-only/master collaboration.
	if a.Channel == "api" && (jobID == 0 || ref.Type != objectref.TypeJob && ref.Type != objectref.TypeEstimate && ref.Type != objectref.TypeInvoice) {
		return "", ErrNotFound
	}
	if a.Channel == "api" {
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL)`, jobID, p.CompanyID).Scan(&eligible); err != nil {
			return "", err
		}
		if !active || !eligible {
			return "", ErrNotFound
		}
	}
	return scope, nil
}

func (s *Service) Comments(ctx context.Context, a Actor, ref objectref.Ref) (Comments, error) {
	result := Comments{Entries: []Comment{}}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var err error
		result, err = readComments(ctx, tx, p, a, ref)
		return err
	})
	return result, err
}

// readComments is also used within the coherent synchronization transaction.
// Both surfaces share parent authorization, placeholders and action capabilities.
func readComments(ctx context.Context, tx pgx.Tx, p Principal, a Actor, ref objectref.Ref) (Comments, error) {
	result := Comments{Entries: []Comment{}}
	err := func() error {
		scope, err := commentParent(ctx, tx, p, a, ref, false)
		if err != nil {
			return err
		}
		_, writeErr := commentParent(ctx, tx, p, a, ref, true)
		if writeErr != nil && !errors.Is(writeErr, ErrConflict) {
			return writeErr
		}
		writable := writeErr == nil
		result.CanAdd = writable && p.Allows("comment.add", scope)
		if a.Channel == "api" {
			result.EligibilityRevision, err = commentEligibility(ctx, tx, p, a, ref)
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT c.id,c.author_id,coalesce(u.name,'Unknown'),CASE WHEN c.removed_at IS NULL THEN c.content ELSE '' END,c.created_at,c.reply_to,c.removed_at,c.removed_by,coalesce(r.name,'Unknown') FROM comments c LEFT JOIN users u ON u.id=c.author_id LEFT JOIN users r ON r.id=c.removed_by WHERE c.company_id=$1 AND c.object_type=$2 AND c.object_id=$3 ORDER BY c.created_at,c.id`, p.CompanyID, string(ref.Type), ref.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Comment
			if err := rows.Scan(&c.ID, &c.AuthorID, &c.Author, &c.Content, &c.CreatedAt, &c.ReplyTo, &c.RemovedAt, &c.RemovedBy, &c.Remover); err != nil {
				return err
			}
			c.CanRemove = writable && c.RemovedAt == nil && (a.Channel != "api" || c.AuthorID == a.UserID) && (p.Allows("comment.remove", scope) || c.AuthorID == a.UserID && p.Allows("comment.remove", Own))
			c.CanReply = c.ReplyTo == nil && c.RemovedAt == nil && result.CanAdd
			result.Entries = append(result.Entries, c)
		}
		return rows.Err()
	}()
	return result, err
}

func (s *Service) AddComment(ctx context.Context, a Actor, ref objectref.Ref, content string, replyTo int64) (int64, error) {
	receipt, err := s.SubmitComment(ctx, a, ref, CommentCommand{OperationID: uuid.NewString(), Content: content, ReplyTo: replyTo})
	return receipt.CommentID, err
}

func applyComment(ctx context.Context, tx pgx.Tx, p Principal, a Actor, ref objectref.Ref, in CommentCommand) (int64, error) {
	var id int64
	if in.RemoveID == 0 {
		content, replyTo := in.Content, in.ReplyTo
		err := func() error {
			scope, err := commentParent(ctx, tx, p, a, ref, true)
			if err != nil {
				return err
			}
			if !p.Allows("comment.add", scope) {
				return ErrForbidden
			}
			if err := validateCommentEligibility(ctx, tx, p, a, ref, in); err != nil {
				return err
			}
			if !utf8.ValidString(content) || strings.ContainsRune(content, 0) || utf8.RuneCountInString(content) > 10000 || strings.TrimSpace(content) == "" {
				return ErrCommentInvalid
			}
			if replyTo != 0 {
				var open bool
				err := tx.QueryRow(ctx, `SELECT reply_to IS NULL AND removed_at IS NULL FROM comments WHERE id=$1 AND company_id=$2 AND object_type=$3 AND object_id=$4 FOR UPDATE`, replyTo, p.CompanyID, string(ref.Type), ref.ID).Scan(&open)
				if errors.Is(err, pgx.ErrNoRows) || err == nil && !open {
					return ErrConflict
				}
				if err != nil {
					return err
				}
			}
			if err := tx.QueryRow(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content,reply_to) VALUES($1,$2,$3,$4,$5,nullif($6,0)) RETURNING id`, p.CompanyID, string(ref.Type), ref.ID, a.UserID, content, replyTo).Scan(&id); err != nil {
				return err
			}
			return commentAudit(ctx, tx, a, p, "comment.add", scope, ref, id, in)
		}()
		return id, err
	}
	id = in.RemoveID
	err := func() error {
		scope, err := commentParent(ctx, tx, p, a, ref, true)
		if err != nil {
			return err
		}
		var author int64
		var removed bool
		err = tx.QueryRow(ctx, `SELECT author_id,removed_at IS NOT NULL FROM comments WHERE id=$1 AND company_id=$2 AND object_type=$3 AND object_id=$4 FOR UPDATE`, id, p.CompanyID, string(ref.Type), ref.ID).Scan(&author, &removed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if a.Channel == "api" && author != a.UserID {
			return ErrForbidden
		}
		if !p.Allows("comment.remove", scope) {
			if author != a.UserID || !p.Allows("comment.remove", Own) {
				return ErrForbidden
			}
			scope = Own
		}
		if err := validateCommentEligibility(ctx, tx, p, a, ref, in); err != nil {
			return err
		}
		if removed {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE comments SET removed_at=clock_timestamp(),removed_by=$2 WHERE id=$1`, id, a.UserID); err != nil {
			return err
		}
		return commentAudit(ctx, tx, a, p, "comment.remove", scope, ref, id, in)
	}()
	return id, err
}

func commentAudit(ctx context.Context, tx pgx.Tx, a Actor, p Principal, permission string, scope Scope, ref objectref.Ref, id int64, in CommentCommand) error {
	detail := map[string]any{"comment_id": id, "operation_id": in.OperationID}
	if in.ParentRevision != "" {
		detail["parent_revision"] = in.ParentRevision
	}
	if in.OccurredAt != nil {
		detail["occurred_at"] = in.OccurredAt
	}
	if a.SessionHash != "" {
		var sessionID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM sessions WHERE token_hash=$1 AND user_id=$2`, a.SessionHash, a.UserID).Scan(&sessionID); err != nil {
			return err
		}
		detail["session_id"] = sessionID
	}
	if err := audit(ctx, tx, a, p, permission, scope, string(ref.Type), ref.ID, detail); err != nil {
		return err
	}
	action := "comment_added"
	if permission == "comment.remove" {
		action = "comment_deleted"
	}
	_, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5,jsonb_build_object('comment_id',$6::bigint,'actor_name',(SELECT name FROM users WHERE id=$2 AND company_id=$1)))`, p.CompanyID, a.UserID, action, string(ref.Type), ref.ID, id)
	return err
}

func (s *Service) RemoveComment(ctx context.Context, a Actor, ref objectref.Ref, id int64) error {
	_, err := s.SubmitComment(ctx, a, ref, CommentCommand{OperationID: uuid.NewString(), RemoveID: id})
	return err
}

// RetainedComment requires both sealed audit View and current parent View. The
// read itself is audited in the same transaction; it is not a field API action.
func (s *Service) RetainedComment(ctx context.Context, a Actor, ref objectref.Ref, id int64) (string, error) {
	var text string
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := commentParent(ctx, tx, p, a, ref, false); err != nil {
			return err
		}
		if a.Channel == "api" || !p.Allows("audit.view", Sealed) {
			return ErrForbidden
		}
		err := tx.QueryRow(ctx, `SELECT content FROM comments WHERE id=$1 AND company_id=$2 AND object_type=$3 AND object_id=$4 AND removed_at IS NOT NULL`, id, p.CompanyID, string(ref.Type), ref.ID).Scan(&text)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "audit.view", Sealed, string(ref.Type), ref.ID, map[string]any{"comment_id": id})
	})
	return text, err
}

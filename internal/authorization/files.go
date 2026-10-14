package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrFileOperation = errors.New("invalid File operation attribution")

// FileOperation is client intent, not device identity or acceptance time. Those
// facts are captured from the authenticated mobile session and database clock.
type FileOperation struct {
	OperationID string     `json:"operation_id"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
}

func (op FileOperation) Validate() error {
	id, err := uuid.Parse(op.OperationID)
	if err != nil || id == uuid.Nil || op.OccurredAt != nil && (op.OccurredAt.IsZero() || op.OccurredAt.After(time.Now())) {
		return ErrFileOperation
	}
	return nil
}

// fileAccess uses the parent's basic View, never financial or closed Context.
// The table names are a closed product allowlist, not request SQL.
func fileAccess(ctx context.Context, q queryer, p Principal, a Actor, ref objectref.Ref, permission string, uploader int64, lock bool) (Scope, error) {
	table := map[string]string{"customer": "customers", "project": "projects", "job": "jobs", "asset": "assets", "estimate": "estimates", "invoice": "invoices", "lease": "leases"}[ref.ObjectType()]
	if table == "" || !ref.Valid() {
		return "", ErrNotFound
	}
	jobExpr, hidden := "0::bigint", "true"
	if ref.ObjectType() == "job" {
		jobExpr = "id"
	}
	if ref.ObjectType() == "estimate" || ref.ObjectType() == "invoice" {
		jobExpr = "coalesce(job_id,0)"
		hidden = "conversion_hidden_at IS NULL"
	}
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var active, visible bool
	var jobID int64
	err := q.QueryRow(ctx, `SELECT deleted_at IS NULL,`+hidden+`,`+jobExpr+` FROM `+table+` WHERE id=$1 AND company_id=$2`+suffix, ref.ID, p.CompanyID).Scan(&active, &visible, &jobID)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !visible {
		return "", ErrNotFound
	}
	if a.Channel == "api" {
		if jobID == 0 {
			return "", ErrNotFound
		}
		if (permission == "file.rename" || permission == "file.remove") && uploader != a.UserID {
			return "", ErrForbidden
		}
	}
	parentView := ref.ObjectType() + ".view"
	all := p.Allows(parentView, All)
	assigned := false
	if jobID != 0 && active && p.Allows(parentView, Assigned) {
		var id int64
		err := q.QueryRow(ctx, `SELECT j.id FROM jobs j JOIN job_assignments ja ON ja.job_id=j.id WHERE j.id=$1 AND ja.user_id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL`+suffix, jobID, a.UserID, p.CompanyID).Scan(&id)
		if err != nil && err != pgx.ErrNoRows {
			return "", err
		}
		assigned = err == nil
	}
	if !all && !assigned || !active && (!all || !p.Allows(ref.ObjectType()+".archived.view", All)) {
		return "", ErrNotFound
	}
	if permission == "" {
		if all {
			return All, nil
		}
		return Assigned, nil
	}
	if permission != "file.download" && !active {
		return "", ErrForbidden
	}
	if p.Allows(permission, All) {
		return All, nil
	}
	if assigned && p.Allows(permission, Assigned) {
		return Assigned, nil
	}
	if uploader == a.UserID && (permission == "file.rename" || permission == "file.remove") && p.Allows(permission, Own) {
		return Own, nil
	}
	return "", ErrForbidden
}

func (s *Service) FileAccess(ctx context.Context, a Actor, ref objectref.Ref, permission string, uploader int64) error {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	_, err = fileAccess(ctx, s.Pool, p, a, ref, permission, uploader, false)
	return err
}

// FileTransferAction records transport facts against their original parent,
// never a fabricated File identity. The transfer UUID is the transport operation
// identity; publication has its own independent operation. Chunk replay is a
// no-op and therefore creates neither bytes nor another immutable audit event.
func (s *Service) FileTransferAction(ctx context.Context, a Actor, ref objectref.Ref, transferID string, fn func(pgx.Tx) (bool, map[string]any, error)) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		// Resolve a caller-scoped collision under the same authority fence as
		// the write. A newly requested parent cannot authorize disclosure of
		// an existing transfer's inaccessible original intent.
		var originalType string
		var originalID int64
		err := tx.QueryRow(ctx, `SELECT object_type,object_id FROM file_transfers WHERE user_id=$1 AND session_hash=$2 AND id=$3 FOR UPDATE`, a.UserID, a.SessionHash, transferID).Scan(&originalType, &originalID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			original, err := objectref.Parse(originalType, originalID)
			if err != nil {
				return ErrNotFound
			}
			if _, err := fileAccess(ctx, tx, p, a, original, "file.upload", a.UserID, true); err != nil {
				return err
			}
		}
		scope, err := fileAccess(ctx, tx, p, a, ref, "file.upload", a.UserID, true)
		if err != nil {
			return err
		}
		changed, detail, err := fn(tx)
		if err != nil || !changed {
			return err
		}
		detail["transfer_id"] = transferID
		var attributed any = detail
		if a.Channel == "api" {
			attributed, err = fileAPIAuditDetail(ctx, tx, a, FileOperation{OperationID: transferID}, detail)
			if err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, "file.upload", scope, ref.ObjectType(), ref.ID, attributed)
	})
}

// FileAction serializes with grant/session changes and locks the exact parent
// and assignment until the File mutation and immutable audit commit together.
// A zero returned effect ID represents a desired-state no-op (no new audit).
func (s *Service) FileAction(ctx context.Context, a Actor, ref objectref.Ref, permission string, uploader int64, fn func(pgx.Tx) (int64, any, error), operation ...FileOperation) error {
	if a.Channel == "api" && len(operation) > 0 {
		if err := operation[0].Validate(); err != nil {
			return err
		}
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		scope, err := fileAccess(ctx, tx, p, a, ref, permission, uploader, true)
		if err != nil {
			return err
		}
		id, detail, err := fn(tx)
		if err != nil {
			return err
		}
		if id == 0 {
			return nil
		}
		if a.Channel == "api" {
			if len(operation) != 1 {
				return ErrFileOperation
			}
			detail, err = fileAPIAuditDetail(ctx, tx, a, operation[0], detail)
			if err != nil {
				return err
			}
		}
		if err := audit(ctx, tx, a, p, permission, scope, "file", id, detail); err != nil {
			return err
		}
		return publishFileActivity(ctx, tx, a, p, ref, permission, id, detail)
	})
}

// This boundary runs only after an authoritative File effect. Transport-stage
// audits do not pass through it. A replay/no-op returned above publishes nothing.
func publishFileActivity(ctx context.Context, tx pgx.Tx, a Actor, p Principal, ref objectref.Ref, permission string, id int64, detail any) error {
	action := ""
	switch permission {
	case "file.upload":
		action = "file_uploaded"
	case "file.rename":
		action = "file_renamed"
	case "file.remove":
		action = "file_deleted"
	default:
		return nil
	}
	var name, actorName string
	var size, revision, uploader int64
	var accepted time.Time
	if err := tx.QueryRow(ctx, `SELECT f.original_name,f.file_size,f.revision,f.uploaded_by,u.name,clock_timestamp() FROM files f JOIN users u ON u.id=$5 AND u.company_id=f.company_id WHERE f.id=$1 AND f.company_id=$2 AND f.object_type=$3 AND f.object_id=$4`, id, p.CompanyID, ref.ObjectType(), ref.ID, a.UserID).Scan(&name, &size, &revision, &uploader, &actorName, &accepted); err != nil {
		return err
	}
	metadata := map[string]any{"file_id": id, "file_name": name, "size": size, "uploaded_by": uploader, "actor_name": actorName}
	if facts, ok := detail.(map[string]any); ok {
		if old, ok := facts["old_name"].(string); ok {
			metadata["old_file_name"] = old
		}
		if stamp, ok := facts["accepted_at"].(time.Time); ok {
			accepted = stamp
		}
		if stamp, ok := facts["occurred_at"].(time.Time); ok {
			metadata["source_occurred_at"] = stamp
		}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.CompanyID, a.UserID, action, ref.ObjectType(), ref.ID, raw, accepted, fmt.Sprintf("file-action:%d:%d", id, revision))
	return err
}

func fileAPIAuditDetail(ctx context.Context, tx pgx.Tx, a Actor, op FileOperation, detail any) (any, error) {
	m, ok := detail.(map[string]any)
	if !ok {
		return nil, ErrFileOperation
	}
	var sessionID int64
	var deviceName string
	var accepted time.Time
	if err := tx.QueryRow(ctx, `SELECT id,coalesce(device_name,''),clock_timestamp() FROM sessions WHERE user_id=$1 AND token_hash=$2 AND kind='mobile' AND revoked_at IS NULL AND expires_at>clock_timestamp()`, a.UserID, a.SessionHash).Scan(&sessionID, &deviceName, &accepted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	m["operation_id"] = uuid.MustParse(op.OperationID).String()
	m["session_id"] = sessionID
	m["device_name"] = deviceName
	m["accepted_at"] = accepted
	m["occurred_at"] = accepted
	m["occurrence_source"] = "server"
	if op.OccurredAt != nil {
		if op.OccurredAt.After(accepted) {
			return nil, ErrFileOperation
		}
		m["occurred_at"] = op.OccurredAt.UTC()
		m["occurrence_source"] = "client"
	}
	return m, nil
}

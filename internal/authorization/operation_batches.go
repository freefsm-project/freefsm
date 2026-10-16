package authorization

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BatchFieldOperation binds the supported independent field actions. Its original
// target and dependency references are intent, not instructions to rewrite later
// operations using an earlier result's identity or revision.
type BatchFieldOperation struct {
	OperationID        string     `json:"operation_id"`
	Type               string     `json:"type"`
	Target             string     `json:"target"`
	DependsOn          []string   `json:"depends_on"`
	NotesRevision      string     `json:"notes_revision"`
	Notes              *string    `json:"field_notes"`
	OccurredAt         *time.Time `json:"occurred_at,omitempty"`
	StatusID           string     `json:"status_id,omitempty"`
	StatusRevision     string     `json:"status_revision,omitempty"`
	MeaningRevision    string     `json:"meaning_revision,omitempty"`
	SubtaskID          string     `json:"subtask_id,omitempty"`
	CompletionRevision string     `json:"completion_revision,omitempty"`
	Completed          *bool      `json:"completed,omitempty"`
}

// Retain the existing Go caller spelling and old serialized Notes intent.
type BatchNotesOperation = BatchFieldOperation

type BatchOperationResult struct {
	OperationID string
	Receipt     *JobExecutionReceipt
	Err         error
}

// SubmitOperationBatch deliberately has no enclosing transaction. Each existing
// domain transaction independently fences current authority and commits its own
// immutable receipt. Cancellation leaves unprocessed identities unreconciled.
func (s *Service) SubmitOperationBatch(ctx context.Context, a Actor, ops []BatchNotesOperation) []BatchOperationResult {
	results := make([]BatchOperationResult, 0, len(ops))
	positions := map[string]int{}
	successful := map[string]bool{}
	for i, op := range ops {
		positions[op.OperationID] = i
	}
	for i, op := range ops {
		id, err := strconv.ParseInt(op.Target, 10, 64)
		if err != nil {
			id, err = s.ResolveSyncedJob(ctx, op.Target)
		}
		if errors.Is(err, ErrNotFound) {
			id = 0
			err = nil
		}
		result := BatchOperationResult{OperationID: op.OperationID, Err: err}
		if err == nil {
			in := JobExecutionCommand{OperationID: op.OperationID, NotesRevision: op.NotesRevision, MissingNotes: op.Type == "job.field_notes" && op.Notes == nil, OccurredAt: op.OccurredAt, StatusRevision: op.StatusRevision, MeaningRevision: op.MeaningRevision}
			in.StatusID, _ = strconv.ParseInt(op.StatusID, 10, 64)
			if op.Notes != nil {
				in.Notes = *op.Notes
			}
			batch := batchExecution{operation: op, positions: positions, index: i, successful: successful}
			var receipt JobExecutionReceipt
			var e error
			switch op.Type {
			case "job.subtask":
				action := ""
				if op.Completed != nil {
					action = "reopen"
					if *op.Completed {
						action = "complete"
					}
				}
				sub, err := s.submitSubtaskBound(ctx, a, id, SubtaskCommand{OperationID: op.OperationID, Action: action, SubtaskID: op.SubtaskID, CompletionRevision: op.CompletionRevision, OccurredAt: op.OccurredAt}, &batch)
				e = err
				receipt = JobExecutionReceipt{OperationID: sub.OperationID, Code: sub.Code, EffectID: sub.SubtaskID, AcceptedAt: sub.AcceptedAt}
			case "job.status":
				receipt, e = s.submitJobExecutionBound(ctx, a, id, "status", in, &batch)
			default:
				receipt, e = s.submitJobExecutionBound(ctx, a, id, "field_notes", in, &batch)
			}
			result.Err = e
			if receipt.Code != "" {
				result.Receipt = &receipt
			}
		}
		results = append(results, result)
		successful[op.OperationID] = result.Err == nil && result.Receipt != nil && result.Receipt.Code == "accepted"
	}
	return results
}

type batchExecution struct {
	operation  BatchNotesOperation
	positions  map[string]int
	index      int
	successful map[string]bool
}

// Validate only after immutable own-receipt recovery, inside the committing
// authority transaction. Submitted position is eligibility, never part of intent;
// a same-intent retry may submit only the unresolved operation.
func (b *batchExecution) validate(ctx context.Context, tx pgx.Tx, a Actor) error {
	if b.operation.Type != "job.field_notes" && b.operation.Type != "job.status" && b.operation.Type != "job.subtask" {
		return &JobExecutionConflict{"invalid_intent"}
	}
	if at := b.operation.OccurredAt; at != nil && at.Nanosecond()%1000 != 0 {
		return &JobExecutionConflict{"invalid_occurrence"}
	}
	for _, dep := range b.operation.DependsOn {
		id, err := uuid.Parse(dep)
		if err != nil || id == uuid.Nil || id.String() != dep {
			return &JobExecutionConflict{"invalid_dependency"}
		}
		if position, inBatch := b.positions[dep]; inBatch && position >= b.index {
			return &JobExecutionConflict{"invalid_dependency"}
		}
	}
	for _, dep := range b.operation.DependsOn {
		if _, earlier := b.positions[dep]; earlier && !b.successful[dep] {
			return &JobExecutionConflict{"failed_dependency"}
		}
		code, err := boundDependencyCode(ctx, tx, a, dep)
		if err != nil {
			return err
		}
		if code == "accepted" {
			continue
		}
		if _, earlier := b.positions[dep]; earlier || code != "" {
			return &JobExecutionConflict{"failed_dependency"}
		}
		return &JobExecutionConflict{"invalid_dependency"}
	}
	return nil
}

// Admission is not success. Consult the owning immutable receipt only after
// verifying deployment/user/client binding. No target representation is loaded.
func boundDependencyCode(ctx context.Context, tx pgx.Tx, a Actor, id string) (string, error) {
	var family string
	err := tx.QueryRow(ctx, `SELECT operation_type FROM operation_admissions WHERE operation_id=$1 AND user_id=$2 AND client_key=$3 AND deployment_id=(SELECT id FROM operation_deployment)`, id, a.UserID, operationClientKey(a)).Scan(&family)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var query string
	switch family {
	case "job.execution":
		query = `SELECT code FROM job_execution_receipts WHERE operation_id=$1`
	case "job.subtask":
		query = `SELECT code FROM subtask_operation_receipts WHERE operation_id=$1`
	case "job.fields":
		query = `SELECT outcome FROM job_field_operations WHERE operation_id=$1`
	case "comment":
		query = `SELECT code FROM comment_operation_receipts WHERE operation_id=$1`
	case "clock":
		query = `SELECT code FROM time_clock_receipts WHERE operation_id=$1`
	case "clock.lease.issue":
		query = `SELECT receipt->>'code' FROM clock_lease_receipts WHERE operation_id=$1`
	case "offline.authorization.issue":
		query = `SELECT receipt->>'code' FROM offline_authorization_receipts WHERE operation_id=$1`
	case "session.revocation":
		query = `SELECT receipt->>'code' FROM session_revocation_receipts WHERE operation_id=$1`
	case "commercial.estimate", "commercial.invoice":
		query = `SELECT receipt->>'outcome' FROM commercial_operations WHERE operation_id=$1`
	case "file.transport.start", "file.transport.chunk":
		query = `SELECT code FROM file_transport_receipts WHERE operation_id=$1`
	case "file.publication", "file.mutation":
		query = `SELECT code FROM file_command_receipts WHERE operation_id=$1
		 UNION ALL SELECT 'accepted' FROM file_publications WHERE operation_id=$1
		 UNION ALL SELECT 'accepted' FROM file_mutation_operations WHERE operation_id=$1
		 LIMIT 1`
	case "service_entry.create", "service_entry.revise":
		query = `SELECT code FROM service_entry_operation_receipts WHERE operation_id=$1`
	default:
		return "", nil
	}
	var code string
	err = tx.QueryRow(ctx, query, id).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return code, err
}

package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BatchFieldOperation carries a supported field operation. Its original
// target and dependency references are intent, not instructions to rewrite later
// operations using an earlier result's identity or revision.
type BatchFieldOperation struct {
	OperationID        string              `json:"operation_id"`
	Type               string              `json:"type"`
	Target             string              `json:"target"`
	DependsOn          []string            `json:"depends_on"`
	NotesRevision      string              `json:"notes_revision"`
	Notes              *string             `json:"field_notes"`
	OccurredAt         *time.Time          `json:"occurred_at,omitempty"`
	StatusID           string              `json:"status_id,omitempty"`
	StatusRevision     string              `json:"status_revision,omitempty"`
	MeaningRevision    string              `json:"meaning_revision,omitempty"`
	SubtaskID          string              `json:"subtask_id,omitempty"`
	CompletionRevision string              `json:"completion_revision,omitempty"`
	Completed          *bool               `json:"completed,omitempty"`
	Revisions          map[string]string   `json:"revisions,omitempty"`
	Values             json.RawMessage     `json:"values,omitempty"`
	Comment            *BatchCommentIntent `json:"comment,omitempty"`
}

type BatchCommentIntent struct {
	ParentType     string  `json:"parent_type"`
	Content        *string `json:"content,omitempty"`
	ReplyTo        string  `json:"reply_to,omitempty"`
	RemoveID       string  `json:"remove_id,omitempty"`
	ParentRevision string  `json:"parent_revision,omitempty"`
}

// Retain the existing Go caller spelling and old serialized Notes intent.
type BatchNotesOperation = BatchFieldOperation

// BatchFileOperation contains metadata intent only. Target is an immutable scoped
// transfer for publication, or the existing File ID for rename/removal.
type BatchFileOperation struct {
	OperationID string     `json:"operation_id"`
	Type        string     `json:"type"`
	Target      string     `json:"target"`
	DependsOn   []string   `json:"depends_on"`
	ObjectType  string     `json:"object_type"`
	ObjectID    int64      `json:"object_id"`
	Revision    string     `json:"revision,omitempty"`
	Name        string     `json:"name,omitempty"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
}

type FileBatchExecutor func(context.Context, BatchFileOperation, func(context.Context, pgx.Tx, Actor) error) (FileReceipt, error)

type BatchOperationResult struct {
	OperationID string
	Receipt     any
	Err         error
}

type BatchClockOperation struct {
	OperationID string       `json:"operation_id"`
	Type        string       `json:"type"`
	DependsOn   []string     `json:"depends_on"`
	Clock       ClockCommand `json:"clock"`
	EntryRef    string       `json:"entry_ref,omitempty"`
	JobRef      string       `json:"job_ref,omitempty"`
}

// A batch member retains its domain-specific reviewed intent. Clock commands
// are not Notes commands and do not acquire revision/target substitution rules.
type BatchOperation struct {
	Job   *BatchFieldOperation
	Clock *BatchClockOperation
	File  *BatchFileOperation
}

func (op BatchOperation) id() string {
	if op.File != nil {
		return op.File.OperationID
	}
	if op.Clock != nil {
		return op.Clock.OperationID
	}
	return op.Job.OperationID
}

// SubmitOperationBatch deliberately has no enclosing transaction. Each existing
// domain transaction independently fences current authority and commits its own
// immutable receipt. Cancellation leaves unprocessed identities unreconciled.
func (s *Service) SubmitOperationBatch(ctx context.Context, a Actor, ops []BatchOperation, files FileBatchExecutor) []BatchOperationResult {
	results := make([]BatchOperationResult, 0, len(ops))
	positions := map[string]int{}
	successful := map[string]bool{}
	for i, member := range ops {
		positions[member.id()] = i
	}
	for i, member := range ops {
		if member.File != nil {
			op := *member.File
			batch := batchExecution{operation: BatchNotesOperation{OperationID: op.OperationID, Type: op.Type, Target: op.Target, DependsOn: op.DependsOn, OccurredAt: op.OccurredAt}, dependencies: batchDependencies{dependsOn: op.DependsOn, positions: positions, index: i, successful: successful}}
			receipt, err := files(ctx, op, batch.validate)
			result := BatchOperationResult{OperationID: op.OperationID, Err: err}
			if receipt.Code != "" {
				result.Receipt = receipt
			}
			results = append(results, result)
			successful[op.OperationID] = err == nil && receipt.Code == "accepted"
			continue
		}
		if member.Clock != nil {
			op := member.Clock
			batch := clockBatchExecution{operation: *op, dependencies: batchDependencies{dependsOn: op.DependsOn, positions: positions, index: i, successful: successful}}
			command := op.Clock
			command.OperationID = op.OperationID
			receipt, err := s.submitClockBound(ctx, a, op.Type == "clock.out", command, &batch)
			result := BatchOperationResult{OperationID: op.OperationID, Err: err}
			if receipt.Code != "" {
				result.Receipt = &receipt
			}
			results = append(results, result)
			successful[op.OperationID] = err == nil && receipt.Code == "accepted"
			continue
		}
		op := *member.Job
		batch := batchExecution{operation: op, dependencies: batchDependencies{dependsOn: op.DependsOn, positions: positions, index: i, successful: successful}}
		if op.Comment != nil {
			in := CommentCommand{OperationID: op.OperationID, OccurredAt: op.OccurredAt, ParentRevision: op.Comment.ParentRevision, requireParentRevision: true}
			if op.Comment.Content != nil {
				in.Content = *op.Comment.Content
			}
			receipt, err := s.submitCommentBound(ctx, a, objectref.Ref{Type: objectref.Type(op.Comment.ParentType)}, in, &batch)
			result := BatchOperationResult{OperationID: op.OperationID, Err: err}
			if receipt.Code != "" {
				result.Receipt = &receipt
			}
			results = append(results, result)
			successful[op.OperationID] = err == nil && receipt.Code == "accepted"
			continue
		}
		id, err := strconv.ParseInt(op.Target, 10, 64)
		if err != nil {
			id, err = s.ResolveSyncedJob(ctx, op.Target)
		}
		if errors.Is(err, ErrNotFound) {
			id = 0
			err = nil
		}
		result := BatchOperationResult{OperationID: op.OperationID, Err: err}
		if err == nil && op.Type == "job.fields" {
			accepted := false
			_, e := s.patchFieldsReviewedBound(ctx, a, id, ReviewedFieldPatch{OperationID: op.OperationID, Revisions: op.Revisions, Values: op.Values, OccurredAt: op.OccurredAt}, &batch)
			result.Err = e
			var conflict *FieldConflict
			if errors.As(e, &conflict) {
				result.Err = executionError(conflict.Code)
			}
			if e == nil || (errors.As(e, &conflict) && conflict.Code != "operation_intent_changed") || errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) {
				receipt, receiptErr := s.JobFieldExecutionReceipt(ctx, a, op.OperationID)
				if receiptErr == nil {
					result.Receipt = &receipt
					accepted = result.Err == nil && receipt.Code == "accepted"
				} else {
					result.Err = receiptErr
				}
			}
			results = append(results, result)
			successful[op.OperationID] = accepted
			continue
		}
		accepted := false
		if err == nil {
			in := JobExecutionCommand{OperationID: op.OperationID, NotesRevision: op.NotesRevision, MissingNotes: op.Type == "job.field_notes" && op.Notes == nil, OccurredAt: op.OccurredAt, StatusRevision: op.StatusRevision, MeaningRevision: op.MeaningRevision}
			in.StatusID, _ = strconv.ParseInt(op.StatusID, 10, 64)
			if op.Notes != nil {
				in.Notes = *op.Notes
			}
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
			accepted = e == nil && receipt.Code == "accepted"
			if receipt.Code != "" {
				result.Receipt = &receipt
			}
		}
		results = append(results, result)
		successful[op.OperationID] = accepted
	}
	return results
}

type batchExecution struct {
	operation    BatchNotesOperation
	dependencies batchDependencies
}

type clockBatchExecution struct {
	operation    BatchClockOperation
	dependencies batchDependencies
}

type batchDependencies struct {
	dependsOn  []string
	positions  map[string]int
	index      int
	successful map[string]bool
}

// Validate only after immutable own-receipt recovery, inside the committing
// authority transaction. Submitted position is eligibility, never part of intent;
// a same-intent retry may submit only the unresolved operation.
func (b *batchExecution) validate(ctx context.Context, tx pgx.Tx, a Actor) error {
	if b.operation.Type != "job.field_notes" && b.operation.Type != "job.status" && b.operation.Type != "job.subtask" && b.operation.Type != "job.fields" && b.operation.Type != "file.publish" && b.operation.Type != "file.rename" && b.operation.Type != "file.remove" && b.operation.Type != "comment.add" && b.operation.Type != "comment.remove" {
		return &JobExecutionConflict{"invalid_intent"}
	}
	if at := b.operation.OccurredAt; at != nil && at.Nanosecond()%1000 != 0 {
		return &JobExecutionConflict{"invalid_occurrence"}
	}
	return b.dependencies.validate(ctx, tx, a)
}

func (b *batchDependencies) validate(ctx context.Context, tx pgx.Tx, a Actor) error {
	for _, dep := range b.dependsOn {
		id, err := uuid.Parse(dep)
		if err != nil || id == uuid.Nil || id.String() != dep {
			return &JobExecutionConflict{"invalid_dependency"}
		}
		if position, inBatch := b.positions[dep]; inBatch && position >= b.index {
			return &JobExecutionConflict{"invalid_dependency"}
		}
	}
	for _, dep := range b.dependsOn {
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

package authorization

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Service Entry commands carry their own relationship and revision witnesses;
// they never inherit target identities from a predecessor's batch result.
type BatchServiceEntryOperation struct {
	OperationID string     `json:"operation_id"`
	Type        string     `json:"type"`
	DependsOn   []string   `json:"depends_on"`
	JobID       string     `json:"job_id"`
	AssetID     string     `json:"asset_id"`
	EntryID     string     `json:"entry_id,omitempty"`
	Revision    string     `json:"revision,omitempty"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
	ServiceEntryInput
}

type serviceEntryBatchExecution struct {
	operation    BatchServiceEntryOperation
	dependencies batchDependencies
}

func (b *serviceEntryBatchExecution) command(ctx context.Context, tx pgx.Tx, a Actor) (ServiceEntryCommand, error) {
	op := b.operation
	in := ServiceEntryCommand{OperationID: op.OperationID, Revision: op.Revision, OccurredAt: op.OccurredAt, Content: op.ServiceEntryInput}
	if err := b.dependencies.validate(ctx, tx, a); err != nil {
		return in, err
	}
	if op.OccurredAt != nil && op.OccurredAt.Nanosecond()%1000 != 0 {
		return in, executionError("invalid_occurrence")
	}
	resolve := func(kind, ref string) (int64, error) {
		if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
			return id, nil
		}
		id, err := uuid.Parse(ref)
		if err != nil {
			return 0, ErrNotFound
		}
		var installed int64
		err = tx.QueryRow(ctx, `SELECT installed_id FROM job_sync_identities WHERE resource_type=$1 AND opaque_id=$2`, kind, id.String()).Scan(&installed)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return installed, err
	}
	var err error
	if in.JobID, err = resolve("job", op.JobID); err != nil {
		return in, err
	}
	if in.AssetID, err = resolve("asset", op.AssetID); err != nil {
		return in, err
	}
	if op.Type == "service_entry.revise" {
		in.EntryID, err = resolve("service_entries", op.EntryID)
	}
	return in, err
}

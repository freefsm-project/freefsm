package authorization

import (
	"context"
	"encoding/json"
)

// BatchCommercialOperation preserves the submitted target and dependency list.
// The existing commercial owner canonicalizes the domain intent and commits the
// dependency decision in the same transaction as its effect and receipt.
type BatchCommercialOperation struct {
	OperationID string          `json:"operation_id"`
	Type        string          `json:"type"`
	Target      string          `json:"target"`
	DependsOn   []string        `json:"depends_on"`
	Commercial  json.RawMessage `json:"commercial"`
}

type CommercialBatchExecutor func(context.Context, BatchCommercialOperation) (CommercialReceipt, error)

type commercialBatchKey struct{}

type commercialBatchExecution struct {
	operation    BatchCommercialOperation
	dependencies batchDependencies
}

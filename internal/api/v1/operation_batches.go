package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

// Read the entire bounded envelope before dispatch. In particular, trailing
// whitespace past the byte bound cannot turn an oversized request into a prefix
// of accepted operations. File bytes use their separate transfer endpoints.
func (h *handler) operationBatch(w http.ResponseWriter, r *http.Request) {
	if w.Header().Get("X-Request-ID") == "" {
		w.Header().Set("X-Request-ID", uuid.NewString())
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Batch requests have no query parameters")
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		writeError(w, 415, "unsupported_media_type", "Send decoded application/json without content encoding")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		writeError(w, 400, "invalid_request", "Incomplete batch envelope")
		return
	}
	if len(raw) > maxRequestBodyBytes {
		writeError(w, 413, "request_too_large", "Batch exceeds 1 MiB decoded JSON")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var envelope struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if !decodeExactJSON(w, r, &envelope, "operations") {
		return
	}
	if len(envelope.Operations) > 100 {
		writeError(w, 413, "request_too_large", "Batch exceeds 100 operations")
		return
	}
	if envelope.Operations == nil {
		writeError(w, 400, "invalid_request", "operations must be an array")
		return
	}
	operations := make([]authorization.BatchOperation, 0, len(envelope.Operations))
	seen := map[string]bool{}
	for _, raw := range envelope.Operations {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			writeError(w, 400, "invalid_request", "Invalid batch member")
			return
		}
		if kind.Type == "clock.in" || kind.Type == "clock.out" {
			op, ok := decodeBatchClock(w, r, raw)
			if !ok {
				return
			}
			if seen[op.OperationID] {
				writeError(w, 400, "invalid_request", "Duplicate operation identity")
				return
			}
			seen[op.OperationID] = true
			operations = append(operations, authorization.BatchOperation{Clock: op})
			continue
		}
		var wire struct {
			authorization.BatchNotesOperation
			DependsOn []*string `json:"depends_on"`
		}
		part := r.Clone(r.Context())
		part.Body = io.NopCloser(bytes.NewReader(raw))
		if !decodeExactJSON(w, part, &wire, "operation_id", "type", "target", "depends_on", "notes_revision", "field_notes", "occurred_at") {
			return
		}
		op := wire.BatchNotesOperation
		if wire.DependsOn != nil {
			op.DependsOn = make([]string, 0, len(wire.DependsOn))
		}
		for _, dependency := range wire.DependsOn {
			// JSON null decodes silently to the zero value of a string. Preserve
			// wire shape here; malformed string UUIDs remain domain outcomes.
			if dependency == nil {
				writeError(w, 400, "invalid_request", "Dependencies must be strings")
				return
			}
			op.DependsOn = append(op.DependsOn, *dependency)
		}
		id, e := uuid.Parse(op.OperationID)
		if e != nil || id == uuid.Nil || id.String() != op.OperationID || seen[op.OperationID] || op.Type != "job.field_notes" || op.Target == "" || op.Notes == nil || op.DependsOn == nil || op.NotesRevision == "" {
			writeError(w, 400, "invalid_request", "Each operation needs a unique canonical UUID and a complete supported Notes intent")
			return
		}
		seen[op.OperationID] = true
		operations = append(operations, authorization.BatchOperation{Notes: &op})
	}
	results := req.Service.SubmitOperationBatch(r.Context(), req.Actor, operations)
	out := make([]batchResult, 0, len(results))
	for _, result := range results {
		item := batchResult{OperationID: result.OperationID, Receipt: result.Receipt}
		if result.Err != nil {
			status, code := 503, "retryable_failure"
			var conflict *authorization.JobExecutionConflict
			var clockConflict *authorization.ClockConflict
			switch {
			case errors.As(result.Err, &clockConflict):
				status, code = 409, clockConflict.Code
				if code == "invalid_dependency" || code == "invalid_occurrence" || code == "invalid_gps" || code == "invalid_clock" || code == "invalid_lease" {
					status = 400
				}
			case errors.As(result.Err, &conflict):
				status, code = 409, conflict.Code
				if code == "invalid_intent" || code == "invalid_occurrence" || code == "invalid_dependency" {
					status = 400
				}
			case errors.Is(result.Err, authorization.ErrForbidden):
				status, code = 403, "forbidden"
			case errors.Is(result.Err, authorization.ErrNotFound):
				status, code = 404, "not_found"
			}
			disposition := "review"
			retry := 0
			if status == 503 {
				disposition = "retry"
				retry = 5
			}
			item.Error = &apiError{Status: status, Code: code, Message: "Reconcile the original receipt; repair terminal intent explicitly", RequestID: w.Header().Get("X-Request-ID"), Details: map[string]any{}, RecoveryDisposition: disposition, RetryAfterSeconds: retry}
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

type batchResult struct {
	OperationID string    `json:"operation_id"`
	Receipt     any       `json:"receipt,omitempty"`
	Error       *apiError `json:"error,omitempty"`
}

func decodeBatchClock(w http.ResponseWriter, r *http.Request, raw []byte) (*authorization.BatchClockOperation, bool) {
	var wire struct {
		OperationID string          `json:"operation_id"`
		Type        string          `json:"type"`
		DependsOn   []*string       `json:"depends_on"`
		Clock       *batchClockWire `json:"clock"`
	}
	part := r.Clone(r.Context())
	part.Body = io.NopCloser(bytes.NewReader(raw))
	if !decodeExactJSON(w, part, &wire, "operation_id", "type", "depends_on", "clock") {
		return nil, false
	}
	id, err := uuid.Parse(wire.OperationID)
	if err != nil || id == uuid.Nil || id.String() != wire.OperationID || wire.DependsOn == nil || wire.Clock == nil || wire.Clock.OperationID != "" {
		writeError(w, 400, "invalid_request", "Clock batch requires canonical operation identity, dependencies and clock intent")
		return nil, false
	}
	dependencies := []string{}
	for _, dep := range wire.DependsOn {
		if dep == nil {
			writeError(w, 400, "invalid_request", "Dependencies must be strings")
			return nil, false
		}
		dependencies = append(dependencies, *dep)
	}
	return &authorization.BatchClockOperation{OperationID: wire.OperationID, Type: wire.Type, DependsOn: dependencies, Clock: wire.Clock.ClockCommand, EntryRef: wire.Clock.EntryRef, JobRef: wire.Clock.JobRef}, true
}

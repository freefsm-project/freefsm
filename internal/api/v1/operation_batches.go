package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

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
		var discriminator struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &discriminator) != nil {
			writeError(w, 400, "invalid_request", "Invalid operation")
			return
		}
		if strings.HasPrefix(discriminator.Type, "file.") {
			var wire struct {
				authorization.BatchFileOperation
				DependsOn []*string `json:"depends_on"`
			}
			part := r.Clone(r.Context())
			part.Body = io.NopCloser(bytes.NewReader(raw))
			properties := []string{"operation_id", "type", "target", "depends_on", "object_type", "object_id", "occurred_at"}
			if discriminator.Type == "file.rename" || discriminator.Type == "file.remove" {
				properties = append(properties, "revision")
			}
			if discriminator.Type == "file.rename" {
				properties = append(properties, "name")
			}
			if !decodeExactJSON(w, part, &wire, properties...) {
				return
			}
			op := wire.BatchFileOperation
			if wire.DependsOn != nil {
				op.DependsOn = []string{}
			}
			for _, dep := range wire.DependsOn {
				if dep == nil {
					writeError(w, 400, "invalid_request", "Dependencies must be strings")
					return
				}
				op.DependsOn = append(op.DependsOn, *dep)
			}
			id, e := uuid.Parse(op.OperationID)
			validTarget := false
			if op.Type == "file.publish" {
				target, targetErr := uuid.Parse(op.Target)
				validTarget = targetErr == nil && target != uuid.Nil && target.String() == op.Target
			}
			if op.Type == "file.rename" || op.Type == "file.remove" {
				target, targetErr := strconv.ParseInt(op.Target, 10, 64)
				validTarget = targetErr == nil && target > 0 && strconv.FormatInt(target, 10) == op.Target && op.Revision != "" && (op.Type == "file.remove" || op.Name != "")
			}
			if e != nil || id == uuid.Nil || id.String() != op.OperationID || seen[op.OperationID] || !validTarget || op.DependsOn == nil || !validTransferParent(op.ObjectType, op.ObjectID) {
				writeError(w, 400, "invalid_request", "A complete canonical File intent is required")
				return
			}
			seen[op.OperationID] = true
			operations = append(operations, authorization.BatchOperation{File: &op})
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
	results := req.Service.SubmitOperationBatch(r.Context(), req.Actor, operations, h.deps.Files.SubmitBatchOperation)
	out := make([]batchResult, 0, len(results))
	for _, result := range results {
		item := batchResult{OperationID: result.OperationID, Receipt: result.Receipt}
		if result.Err != nil {
			status, code := 503, "retryable_failure"
			var conflict *authorization.JobExecutionConflict
			var validation *authorization.FileValidationError
			switch {
			case errors.As(result.Err, &conflict):
				status, code = 409, conflict.Code
				if code == "invalid_intent" || code == "invalid_occurrence" || code == "invalid_dependency" {
					status = 400
				}
			case errors.Is(result.Err, authorization.ErrForbidden):
				status, code = 403, "forbidden"
			case errors.Is(result.Err, authorization.ErrNotFound):
				status, code = 404, "not_found"
			case errors.Is(result.Err, authorization.ErrConflict):
				status, code = 409, "file_conflict"
			case errors.Is(result.Err, authorization.ErrFileOperation), errors.As(result.Err, &validation):
				status, code = 400, "invalid_file"
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

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
		var wire struct {
			authorization.BatchOperation
			DependsOn []*string       `json:"depends_on"`
			Comment   json.RawMessage `json:"comment"`
		}
		part := r.Clone(r.Context())
		part.Body = io.NopCloser(bytes.NewReader(raw))
		if !decodeExactJSON(w, part, &wire, "operation_id", "type", "target", "depends_on", "notes_revision", "field_notes", "occurred_at", "comment") {
			return
		}
		op := wire.BatchOperation
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		validIntent := false
		switch op.Type {
		case "job.field_notes":
			validIntent = wire.Comment == nil && op.Notes != nil && op.NotesRevision != ""
		case "comment.add", "comment.remove":
			if wire.Comment == nil {
				break
			}
			part.Body = io.NopCloser(bytes.NewReader(wire.Comment))
			var intent authorization.BatchCommentIntent
			if !decodeExactJSON(w, part, &intent, "parent_type", "content", "reply_to", "remove_id") {
				return
			}
			var supplied map[string]json.RawMessage
			_ = json.Unmarshal(wire.Comment, &supplied)
			validIntent = (intent.ParentType == "job" || intent.ParentType == "estimate" || intent.ParentType == "invoice") && fields["notes_revision"] == nil && fields["field_notes"] == nil
			if op.Type == "comment.add" {
				validIntent = validIntent && intent.Content != nil && supplied["remove_id"] == nil && (supplied["reply_to"] == nil || intent.ReplyTo != "")
			} else {
				validIntent = validIntent && intent.RemoveID != "" && supplied["content"] == nil && supplied["reply_to"] == nil
			}
			op.Comment = &intent
		}
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
		if e != nil || id == uuid.Nil || id.String() != op.OperationID || seen[op.OperationID] || op.Target == "" || op.DependsOn == nil || !validIntent {
			writeError(w, 400, "invalid_request", "Each operation needs a unique canonical UUID and a complete supported intent")
			return
		}
		seen[op.OperationID] = true
		operations = append(operations, op)
	}
	results := req.Service.SubmitOperationBatch(r.Context(), req.Actor, operations)
	out := make([]batchResult, 0, len(results))
	for _, result := range results {
		item := batchResult{OperationID: result.OperationID, Receipt: result.Receipt}
		if result.Err != nil {
			status, code := 503, "retryable_failure"
			var conflict *authorization.JobExecutionConflict
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
			case errors.Is(result.Err, authorization.ErrCommentInvalid):
				status, code = 400, "invalid_comment"
			case errors.Is(result.Err, authorization.ErrConflict):
				status, code = 409, "comment_conflict"
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

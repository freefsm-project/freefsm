package v1

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const maxRequestBodyBytes = 1 << 20

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Status              int            `json:"status"`
	RequestID           string         `json:"request_id"`
	Details             map[string]any `json:"details"`
	RetryAfterSeconds   int            `json:"retry_after_seconds,omitempty"`
	Code                string         `json:"code"`
	Message             string         `json:"message"`
	RecoveryDisposition string         `json:"recovery_disposition,omitempty"`
}

type responseEnvelope struct {
	Data any `json:"data"`
}

// WriteUnavailable preserves the API error contract when outer instance
// admission or connection recovery rejects a request before this router runs.
func WriteUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeError(w, http.StatusServiceUnavailable, "unavailable", "API temporarily unavailable; retry after the indicated delay")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(responseEnvelope{Data: value})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		id = uuid.NewString()
		w.Header().Set("X-Request-ID", id)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	disposition := "review"
	if status == 429 || status >= 500 {
		disposition = "retry"
		// Dependency failures must carry usable timing even when the rejecting
		// service did not provide a more specific backoff. Preserve rate-limit
		// middleware's existing Retry-After when present.
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", "5")
		}
	}
	if status == 401 || code == "password_change_required" {
		disposition = "purge"
	}
	if code == "instance_restored" {
		disposition = "locked_review"
	}
	retry, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{Status: status, Code: code, Message: message, RecoveryDisposition: disposition, RequestID: id, Details: map[string]any{}, RetryAfterSeconds: retry}})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 1 MiB")
			return false
		}
		message := "request body must contain a valid JSON object"
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			message = err.Error()
		}
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain a single JSON object")
		return false
	}
	return true
}

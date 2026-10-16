package v1

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// Preserve the original nested bytes until exact names and duplicate members
// have been checked. Domain-invalid values still belong to the owning service;
// malformed object shapes must never admit an earlier batch member.
func fieldPatchWire(w http.ResponseWriter, r *http.Request, values, revisions json.RawMessage) (map[string]string, bool) {
	part := func(raw json.RawMessage) *http.Request {
		p := r.Clone(r.Context())
		p.Body = io.NopCloser(bytes.NewReader(raw))
		return p
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(values, &entries); err != nil || entries == nil {
		writeError(w, 400, "invalid_json", "Field values must be an array")
		return nil, false
	}
	for _, entry := range entries {
		var value struct {
			DefinitionID *int64          `json:"definition_id"`
			Value        json.RawMessage `json:"value"`
		}
		if !decodeExactJSON(w, part(entry), &value, "definition_id", "value") {
			return nil, false
		}
		if value.DefinitionID == nil || *value.DefinitionID < 1 || value.Value == nil {
			writeError(w, 400, "invalid_json", "Field identity and value are required")
			return nil, false
		}
		var typed any
		if err := json.Unmarshal(value.Value, &typed); err != nil {
			writeError(w, 400, "invalid_json", "Invalid field value")
			return nil, false
		}
		switch v := typed.(type) {
		case string:
		case []any:
			seen := map[string]bool{}
			for _, entry := range v {
				text, ok := entry.(string)
				if !ok || seen[text] {
					writeError(w, 400, "invalid_json", "Selections must be distinct strings")
					return nil, false
				}
				seen[text] = true
			}
		default:
			writeError(w, 400, "invalid_json", "Field values must be strings or string arrays")
			return nil, false
		}
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(revisions, &members); err != nil || members == nil {
		writeError(w, 400, "invalid_json", "Revisions must be an object")
		return nil, false
	}
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	var wire map[string]*string
	if !decodeExactJSON(w, part(revisions), &wire, keys...) {
		return nil, false
	}
	out := make(map[string]string, len(wire))
	for key, value := range wire {
		if value == nil {
			writeError(w, 400, "invalid_json", "Revision tokens must be strings")
			return nil, false
		}
		out[key] = *value
	}
	return out, true
}

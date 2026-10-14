package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/google/uuid"
)

// The original values are comparison inputs, never authority. Every submitted
// change still needs its read revision under the transaction's authority fence.
func reviewedJobFieldInput(r *http.Request) authorization.ReviewedFieldPatch {
	var original authorization.FieldState
	values := parseCustomFieldValues(r)
	if err := json.Unmarshal([]byte(r.PostForm.Get("field_baseline")), &original); err != nil || original.Revisions == nil || len(original.Values) == 0 {
		// Without an original snapshot, even an empty control may be a change.
		// Pass all explicit intent to the mandatory revision validator rather
		// than acknowledging a clear which was silently discarded as a no-op.
		return authorization.ReviewedFieldPatch{OperationID: r.PostForm.Get("field_operation_id"), Values: json.RawMessage(values)}
	}
	var before, entered []customfields.Value
	if err := json.Unmarshal(original.Values, &before); err != nil || before == nil {
		return authorization.ReviewedFieldPatch{OperationID: r.PostForm.Get("field_operation_id"), Values: json.RawMessage(values)}
	}
	_ = json.Unmarshal([]byte(values), &entered)
	previous := map[int64]any{}
	for _, value := range before {
		previous[value.DefinitionID] = value.Value
	}
	changed := make([]customfields.Value, 0, len(entered))
	for _, value := range entered {
		old, exists := previous[value.DefinitionID]
		if reflect.DeepEqual(old, value.Value) || !exists && customfields.Empty(value.Value) {
			continue
		}
		changed = append(changed, value)
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].DefinitionID < changed[j].DefinitionID })
	encoded, _ := json.Marshal(changed)
	return authorization.ReviewedFieldPatch{OperationID: r.PostForm.Get("field_operation_id"), Revisions: original.Revisions, Values: encoded}
}

func (h *CustomFieldHandler) jobValues(w http.ResponseWriter, r *http.Request, id int64) {
	request, _ := authorization.FromContext(r.Context())
	if r.Method == http.MethodGet && r.URL.Query().Get("operation_id") != "" {
		receipt, err := request.Service.FieldOperationReceipt(r.Context(), request.Actor, id, r.URL.Query().Get("operation_id"))
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
		return
	}
	var state authorization.FieldState
	var err error
	if r.Method == http.MethodGet {
		if err := request.Service.ParentAccess(r.Context(), request.Actor, "job", id, "job.fields.edit"); err != nil {
			authorizationError(w, r, err)
			return
		}
		state, err = request.Service.ReadFieldState(r.Context(), request.Actor, id)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
	}
	message, submitted := "", ""
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		input := reviewedJobFieldInput(r)
		state, err = request.Service.SaveFieldsReviewed(r.Context(), request.Actor, id, input)
		if err == nil {
			http.Redirect(w, r, fmt.Sprintf("/jobs/%d/custom-fields", id), 303)
			return
		}
		var conflict *authorization.FieldConflict
		if !errors.As(err, &conflict) {
			authorizationError(w, r, err)
			return
		}
		// Invalid IDs have no transaction snapshot. Refresh only through View.
		if state.Revisions == nil {
			state, err = request.Service.ReadFieldState(r.Context(), request.Actor, id)
			if err != nil {
				authorizationError(w, r, err)
				return
			}
		}
		message = "Not saved: " + conflict.Code + ". Compare your changes with current values, then explicitly submit a new operation or discard."
		submitted = string(input.Values)
	}
	defs, err := h.svc.ListForObjectType(r.Context(), "job")
	if err != nil {
		http.Error(w, "Unable to load fields", 500)
		return
	}
	baseline, _ := json.Marshal(state)
	fields := buildCustomFieldDisplay(defs, string(state.Values))
	candidate := fields
	if submitted != "" {
		candidate = buildCustomFieldErrorDisplay(defs, string(state.Values), submitted, false)
	}
	w.Header().Set("Cache-Control", "no-store")
	if message != "" {
		w.WriteHeader(409)
	}
	_ = templates.JobFieldsForm(id, candidate, fields, message, uuid.NewString(), string(baseline)).Render(r.Context(), w)
}

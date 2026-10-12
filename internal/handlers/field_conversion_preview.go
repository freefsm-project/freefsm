package handlers

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"strconv"
)

func (h *EstimateHandler) ConversionPreview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	tx, err := bound.Service.Pool.Begin(r.Context())
	if err != nil {
		internalServerError(w, r, "conversion preview", err)
		return
	}
	defer tx.Rollback(r.Context())
	approval, err := bound.Service.AuthorizeConversion(r.Context(), tx, bound.Actor, id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	canEdit := bound.Service.AuthorizeConversionValues(r.Context(), tx, approval) == nil
	var source string
	if err = tx.QueryRow(r.Context(), `SELECT custom_fields::text FROM estimates WHERE id=$1 AND conversion_hidden_at IS NULL`, id).Scan(&source); err != nil {
		http.NotFound(w, r)
		return
	}
	// Release locks before using the Ent read adapter. Commit repeats every check.
	if err = tx.Rollback(r.Context()); err != nil {
		internalServerError(w, r, "conversion preview", err)
		return
	}
	defs, err := h.defSvc.ListAll(r.Context())
	if err != nil {
		internalServerError(w, r, "conversion preview", err)
		return
	}
	byKey := map[string]int64{}
	for _, d := range defs {
		if d.ObjectType == "estimate" && d.ConversionKey != nil && !d.Retired {
			byKey[*d.ConversionKey] = d.ID
		}
	}
	var values []customfields.Value
	if err = json.Unmarshal([]byte(source), &values); err != nil {
		internalServerError(w, r, "conversion preview", err)
		return
	}
	byID := map[int64]any{}
	for _, v := range values {
		byID[v.DefinitionID] = v.Value
	}
	var pairedDefs, entryDefs []*ent.CustomFieldDefinition
	pairedValues := []customfields.Value{}
	for _, d := range defs {
		if d.ObjectType != "invoice" || d.Retired {
			continue
		}
		if d.ConversionKey != nil && byKey[*d.ConversionKey] != 0 {
			if v, ok := byID[byKey[*d.ConversionKey]]; ok {
				pairedDefs = append(pairedDefs, d)
				pairedValues = append(pairedValues, customfields.Value{DefinitionID: d.ID, Value: v})
			} else {
				copy := *d
				copy.DefaultValue = nil
				entryDefs = append(entryDefs, &copy)
			}
		} else {
			entryDefs = append(entryDefs, d)
		}
	}
	encoded, _ := json.Marshal(pairedValues)
	templates.FieldConversionPreview(id, uuid.NewString(), buildCustomFieldDisplay(pairedDefs, string(encoded)), buildCustomFieldInitialDisplay(entryDefs), canEdit).Render(r.Context(), w)
}

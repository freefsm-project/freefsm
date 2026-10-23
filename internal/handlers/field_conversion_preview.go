package handlers

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
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
	preview, err := conversion.New(bound.Service.Pool).Preview(r.Context(), id, nil)
	if err != nil {
		writeConversionError(w, r, err)
		return
	}
	var defs []*ent.CustomFieldDefinition
	if err = json.Unmarshal(preview.Definitions, &defs); err != nil {
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
	if err = json.Unmarshal(preview.SourceFields, &values); err != nil {
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
	var lines []services.LineItem
	if len(preview.LineItems) > 0 {
		if err = json.Unmarshal(preview.LineItems, &lines); err != nil {
			internalServerError(w, r, "conversion preview lines", err)
			return
		}
	}
	templates.FieldConversionPreview(id, uuid.NewString(), preview, lines, buildCustomFieldDisplay(pairedDefs, string(encoded)), buildCustomFieldInitialDisplay(entryDefs)).Render(r.Context(), w)
}

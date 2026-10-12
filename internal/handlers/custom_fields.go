package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type CustomFieldHandler struct {
	svc *services.CustomFieldDefinitionService
}

func NewCustomFieldHandler(svc *services.CustomFieldDefinitionService) *CustomFieldHandler {
	return &CustomFieldHandler{svc: svc}
}

func (h *CustomFieldHandler) List(w http.ResponseWriter, r *http.Request) {
	if !fieldPermission(w, r, false) {
		return
	}
	defs, err := h.svc.ListAll(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	rows := make([]templates.CustomFieldDefRow, len(defs))
	paired := make(map[string]*ent.CustomFieldDefinition)
	for _, d := range defs {
		if d.ConversionKey != nil {
			paired[d.ObjectType+":"+*d.ConversionKey] = d
		}
	}
	for i, d := range defs {
		counterpart := ""
		if d.ConversionKey != nil && (d.ObjectType == "estimate" || d.ObjectType == "invoice") {
			otherType := "estimate"
			if d.ObjectType == "estimate" {
				otherType = "invoice"
			}
			if other := paired[otherType+":"+*d.ConversionKey]; other != nil {
				counterpart = other.Name + " (" + otherType + ")"
			} else {
				counterpart = "Unpaired"
			}
		}
		rows[i] = templates.CustomFieldDefRow{
			HelpText: d.HelpText, IncludePDF: d.IncludePdf,
			Retired:        d.Retired,
			RetiredOptions: d.RetiredOptions,
			ID:             d.ID,
			ObjectType:     d.ObjectType,
			Name:           d.Name,
			FieldType:      d.FieldType,
			Precision:      d.Precision, DefaultValue: stringValue(d.DefaultValue),
			Required:      d.Required,
			Options:       d.Options,
			SortOrder:     d.SortOrder,
			ConversionKey: stringValue(d.ConversionKey),
			Counterpart:   counterpart,
		}
	}

	templates.CustomFieldDefIndex(templates.CustomFieldDefListPageData{
		Definitions: rows,
	}).Render(r.Context(), w)
}

func (h *CustomFieldHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !fieldPermission(w, r, true) {
		return
	}
	if r.Method == http.MethodGet {
		templates.CustomFieldDefForm(newDefFormData()).Render(r.Context(), w)
		return
	}
	h.saveTyped(w, r, 0)
}

func (h *CustomFieldHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !fieldPermission(w, r, true) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		defs, err := h.svc.ListAll(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var found *templates.CustomFieldDefRow
		for _, d := range defs {
			if d.ID == id {
				r := templates.CustomFieldDefRow{
					HelpText: d.HelpText, IncludePDF: d.IncludePdf,
					Retired:        d.Retired,
					RetiredOptions: d.RetiredOptions,
					ID:             d.ID, ObjectType: d.ObjectType, Name: d.Name,
					FieldType: d.FieldType, Required: d.Required,
					Options: d.Options, SortOrder: d.SortOrder, ConversionKey: stringValue(d.ConversionKey), Precision: d.Precision, DefaultValue: stringValue(d.DefaultValue),
				}
				found = &r
				break
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}
		templates.CustomFieldDefForm(templates.CustomFieldDefFormData{
			Def:         *found,
			IsNew:       false,
			ObjectTypes: services.CustomFieldObjectTypes,
			FieldTypes:  services.CustomFieldTypes,
		}).Render(r.Context(), w)
		return
	}
	h.saveTyped(w, r, id)
}

func (h *CustomFieldHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !fieldPermission(w, r, true) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}

	request, _ := authorization.FromContext(r.Context())
	if err := request.Service.DeleteField(r.Context(), request.Actor, id); err != nil {
		if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) {
			authorizationError(w, r, err)
			return
		}
		http.Error(w, err.Error(), 422)
		return
	}
	http.Redirect(w, r, "/settings/custom-fields", 303)
}

func newDefFormData() templates.CustomFieldDefFormData {
	return templates.CustomFieldDefFormData{
		Def: templates.CustomFieldDefRow{
			Options: "[]",
		},
		IsNew:       true,
		ObjectTypes: services.CustomFieldObjectTypes,
		FieldTypes:  services.CustomFieldTypes,
	}
}

func strPtr(s string) *string { return &s }

func buildCustomFieldDisplay(defs []*ent.CustomFieldDefinition, customFieldsJSON string) []templates.CustomFieldDisplay {
	var values []map[string]interface{}
	json.Unmarshal([]byte(customFieldsJSON), &values)

	valueMap := make(map[int64]string)
	selectedMap := make(map[int64][]string)
	for _, v := range values {
		id, _ := v["definition_id"].(float64)
		val, _ := v["value"].(string)
		if values, ok := v["value"].([]any); ok {
			for _, value := range values {
				if text, ok := value.(string); ok {
					selectedMap[int64(id)] = append(selectedMap[int64(id)], text)
				}
			}
			val = strings.Join(selectedMap[int64(id)], ", ")
		}
		valueMap[int64(id)] = val
	}

	result := make([]templates.CustomFieldDisplay, 0, len(defs))
	for _, d := range defs {
		var opts []string
		json.Unmarshal([]byte(d.Options), &opts)
		var retired []string
		json.Unmarshal([]byte(d.RetiredOptions), &retired)

		result = append(result, templates.CustomFieldDisplay{
			HelpText:       d.HelpText,
			Retired:        d.Retired,
			RetiredOptions: retired,
			DefinitionID:   d.ID,
			Precision:      d.Precision,
			Name:           d.Name,
			FieldType:      d.FieldType,
			Value:          valueMap[d.ID],
			Options:        opts,
			Required:       d.Required,
			Selected:       selectedMap[d.ID],
		})
	}
	return result
}

func buildCustomFieldInitialDisplay(defs []*ent.CustomFieldDefinition) []templates.CustomFieldDisplay {
	values := []customfields.Value{}
	for _, d := range defs {
		if d.DefaultValue != nil && !d.Retired {
			v, err := customfields.DefaultValue(customfields.Definition{ID: d.ID, Name: d.Name, Type: d.FieldType, Default: d.DefaultValue})
			if err == nil {
				values = append(values, customfields.Value{DefinitionID: d.ID, Value: v})
			}
		}
	}
	b, _ := json.Marshal(values)
	return buildCustomFieldDisplay(defs, string(b))
}

func (h *JobHandler) relatedCreationFields(ctx context.Context, typ string) []templates.CustomFieldDisplay {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return nil
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	if err != nil || !p.Allows(typ+".create", authorization.All) {
		return nil
	}
	defs, err := h.defSvc.ListForObjectType(ctx, typ)
	if err != nil {
		return nil
	}
	return buildCustomFieldInitialDisplay(defs)
}

// A failed patch must not turn omitted existing values into empty form controls.
// Reconstruct the authorized candidate, retaining explicit empty input over a
// creation default just as the committing validator does.
func buildCustomFieldErrorDisplay(defs []*ent.CustomFieldDefinition, old, submitted string, creating bool) []templates.CustomFieldDisplay {
	values := map[int64]any{}
	if creating {
		for _, d := range defs {
			if d.DefaultValue != nil && !d.Retired {
				v, err := customfields.DefaultValue(customfields.Definition{ID: d.ID, Name: d.Name, Type: d.FieldType, Default: d.DefaultValue})
				if err == nil {
					values[d.ID] = v
				}
			}
		}
	}
	var stored, entered []customfields.Value
	json.Unmarshal([]byte(old), &stored)
	json.Unmarshal([]byte(submitted), &entered)
	for _, v := range stored {
		values[v.DefinitionID] = v.Value
	}
	for _, v := range entered {
		values[v.DefinitionID] = v.Value
	}
	result := make([]customfields.Value, 0, len(defs))
	for _, d := range defs {
		if v, ok := values[d.ID]; ok {
			result = append(result, customfields.Value{DefinitionID: d.ID, Value: v})
		}
	}
	b, _ := json.Marshal(result)
	return buildCustomFieldDisplay(defs, string(b))
}

func parseCustomFieldValues(r *http.Request) string {
	var fields []map[string]interface{}
	for key, values := range r.PostForm {
		if strings.HasPrefix(key, "custom_field_") {
			idStr := strings.TrimSuffix(strings.TrimPrefix(key, "custom_field_"), "[]")
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				return `[{"definition_id":-1,"value":"invalid field key"}]`
			}
			var val any = ""
			if len(values) > 0 {
				val = values[0]
			}
			if len(values) > 1 || strings.HasSuffix(key, "[]") {
				selected := make([]string, 0, len(values))
				for _, value := range values {
					if value != "" {
						selected = append(selected, value)
					}
				}
				val = selected
			}
			fields = append(fields, map[string]interface{}{
				"definition_id": id,
				"value":         val,
			})
		}
	}
	if len(fields) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(fields)
	return string(b)
}

func parseCustomFieldPatch(r *http.Request) *string {
	for name := range r.PostForm {
		if strings.HasPrefix(name, "custom_field_") {
			v := parseCustomFieldValues(r)
			return &v
		}
	}
	return nil
}

func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func fieldPermission(w http.ResponseWriter, r *http.Request, manage bool) bool {
	request, ok := authorization.FromContext(r.Context())
	if !ok || request.Service == nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	permission := "configuration.fields.view"
	if manage {
		permission = "configuration.fields.manage"
	}
	p, err := request.Service.Principal(r.Context(), request.Actor)
	if err != nil {
		authorizationError(w, r, err)
		return false
	}
	if !p.Allows(permission, authorization.Configuration) {
		authorizationError(w, r, authorization.ErrForbidden)
		return false
	}
	return true
}

func (h *CustomFieldHandler) saveTyped(w http.ResponseWriter, r *http.Request, id int64) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	request, _ := authorization.FromContext(r.Context())
	in := authorization.FieldInput{ObjectType: r.PostForm.Get("object_type"), Name: r.PostForm.Get("name"), Type: r.PostForm.Get("field_type"), Options: r.PostForm.Get("options"), ConversionKey: r.PostForm.Get("conversion_key"), Required: r.PostForm.Get("required") == "true"}
	var inputErr error
	in.Retired = r.PostForm.Get("retired") == "true"
	in.RetiredOptions = r.PostForm.Get("retired_options")
	in.HelpText = r.PostForm.Get("help_text")
	in.IncludePDF = r.PostForm.Get("include_pdf") == "true"
	if raw := r.PostForm.Get("sort_order"); raw != "" {
		var err error
		in.SortOrder, err = strconv.Atoi(raw)
		if err != nil || in.SortOrder < 0 {
			inputErr = &customfields.FieldError{Name: "Sort order", Message: "enter a nonnegative integer"}
		}
	}
	if raw := r.PostForm.Get("required"); raw != "" && raw != "true" && raw != "false" {
		inputErr = &customfields.FieldError{Name: "Required", Message: "choose Yes or No"}
	}
	if raw := r.PostForm.Get("precision"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err == nil {
			in.Precision = &v
		} else {
			inputErr = &customfields.FieldError{Name: "Precision", Message: "enter an integer from 0 to 18"}
		}
	}
	if r.PostForm.Has("default_value") && r.PostForm.Get("default_value") != "" {
		v := r.PostForm.Get("default_value")
		in.Default = &v
	}
	err := inputErr
	if err == nil {
		_, err = request.Service.SaveField(r.Context(), request.Actor, id, in)
	}
	if err != nil {
		if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) {
			authorizationError(w, r, err)
			return
		}
		data := newDefFormData()
		data.IsNew = id == 0
		data.Def = templates.CustomFieldDefRow{ID: id, ObjectType: in.ObjectType, Name: in.Name, FieldType: in.Type, Options: in.Options, Required: in.Required, SortOrder: in.SortOrder, ConversionKey: in.ConversionKey, Precision: in.Precision, DefaultValue: stringValue(in.Default)}
		data.Errors = map[string]string{"definition": err.Error()}
		data.Def.Retired = in.Retired
		data.Def.RetiredOptions = in.RetiredOptions
		data.Def.HelpText = in.HelpText
		data.Def.IncludePDF = in.IncludePDF
		w.WriteHeader(http.StatusUnprocessableEntity)
		templates.CustomFieldDefForm(data).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, "/settings/custom-fields", http.StatusSeeOther)
}

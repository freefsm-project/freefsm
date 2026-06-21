package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type CustomFieldHandler struct {
	svc         *services.CustomFieldDefinitionService
	activitySvc *services.ActivityService
	depSvc      *services.DependencyService
}

func NewCustomFieldHandler(svc *services.CustomFieldDefinitionService, activitySvc *services.ActivityService, depSvc *services.DependencyService) *CustomFieldHandler {
	return &CustomFieldHandler{svc: svc, activitySvc: activitySvc, depSvc: depSvc}
}

func (h *CustomFieldHandler) List(w http.ResponseWriter, r *http.Request) {
	defs, err := h.svc.ListAll(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	rows := make([]templates.CustomFieldDefRow, len(defs))
	for i, d := range defs {
		rows[i] = templates.CustomFieldDefRow{
			ID:         d.ID,
			ObjectType: d.ObjectType,
			Name:       d.Name,
			FieldType:  d.FieldType,
			Required:   d.Required,
			Options:    d.Options,
			SortOrder:  d.SortOrder,
		}
	}

	templates.CustomFieldDefIndex(templates.CustomFieldDefListPageData{
		Definitions: rows,
	}).Render(r.Context(), w)
}

func (h *CustomFieldHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.CustomFieldDefForm(newDefFormData()).Render(r.Context(), w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		data := newDefFormData()
		data.Errors = map[string]string{"name": "Name is required"}
		templates.CustomFieldDefForm(data).Render(r.Context(), w)
		return
	}

	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	params := services.CustomFieldDefCreateParams{
		ObjectType: r.FormValue("object_type"),
		Name:       name,
		FieldType:  r.FormValue("field_type"),
		Required:   r.FormValue("required") == "true",
		Options:    r.FormValue("options"),
		SortOrder:  sortOrder,
	}

	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if u, ok := middleware.UserFromContext(r.Context()); ok && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), u.ID, "field_created", "custom_field", result.ID, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}

	http.Redirect(w, r, "/settings/custom-fields?flash=Field+created", http.StatusSeeOther)
}

func (h *CustomFieldHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		defs, err := h.svc.ListAll(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var found *templates.CustomFieldDefRow
		for _, d := range defs {
			if d.ID == id {
				r := templates.CustomFieldDefRow{
					ID: d.ID, ObjectType: d.ObjectType, Name: d.Name,
					FieldType: d.FieldType, Required: d.Required,
					Options: d.Options, SortOrder: d.SortOrder,
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		defs, _ := h.svc.ListAll(r.Context())
		var found *templates.CustomFieldDefRow
		for _, d := range defs {
			if d.ID == id {
				r := templates.CustomFieldDefRow{
					ID: d.ID, ObjectType: d.ObjectType, Name: d.Name,
					FieldType: d.FieldType, Required: d.Required,
					Options: d.Options, SortOrder: d.SortOrder,
				}
				found = &r
				break
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}
		data := templates.CustomFieldDefFormData{
			Def:         *found,
			IsNew:       false,
			Errors:      map[string]string{"name": "Name is required"},
			ObjectTypes: services.CustomFieldObjectTypes,
			FieldTypes:  services.CustomFieldTypes,
		}
		templates.CustomFieldDefForm(data).Render(r.Context(), w)
		return
	}

	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	params := services.CustomFieldDefUpdateParams{
		Name:      &name,
		FieldType: strPtr(r.FormValue("field_type")),
		Required:  boolPtr(r.FormValue("required") == "true"),
		Options:   strPtr(r.FormValue("options")),
		SortOrder: &sortOrder,
	}

	result, err := h.svc.Update(r.Context(), id, params)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if u, ok := middleware.UserFromContext(r.Context()); ok && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), u.ID, "field_updated", "custom_field", result.ID, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  u.Name,
		})
	}

	http.Redirect(w, r, "/settings/custom-fields?flash=Field+updated", http.StatusSeeOther)
}

func (h *CustomFieldHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}

	var entityName string
	if h.activitySvc != nil {
		defs, _ := h.svc.ListAll(r.Context())
		for _, d := range defs {
			if d.ID == id {
				entityName = d.Name
				break
			}
		}
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if u, ok := middleware.UserFromContext(r.Context()); ok && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), u.ID, "field_deleted", "custom_field", id, map[string]interface{}{
			"entity_name": entityName,
			"actor_name":  u.Name,
		})
	}

	http.Redirect(w, r, "/settings/custom-fields?flash=Field+deleted", http.StatusSeeOther)
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
	for _, v := range values {
		id, _ := v["definition_id"].(float64)
		val, _ := v["value"].(string)
		valueMap[int64(id)] = val
	}

	result := make([]templates.CustomFieldDisplay, 0, len(defs))
	for _, d := range defs {
		var opts []string
		json.Unmarshal([]byte(d.Options), &opts)

		result = append(result, templates.CustomFieldDisplay{
			DefinitionID: d.ID,
			Name:         d.Name,
			FieldType:    d.FieldType,
			Value:        valueMap[d.ID],
			Options:      opts,
			Required:     d.Required,
		})
	}
	return result
}

func parseCustomFieldValues(r *http.Request) string {
	var fields []map[string]interface{}
	for key, values := range r.PostForm {
		if strings.HasPrefix(key, "custom_field_") {
			idStr := strings.TrimPrefix(key, "custom_field_")
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				continue
			}
			val := ""
			if len(values) > 0 {
				val = values[0]
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

func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }

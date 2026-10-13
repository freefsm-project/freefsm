package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func jobRelationshipError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrConflict) || errors.Is(err, authorization.ErrPending) || errors.Is(err, authorization.ErrUnavailable) || errors.Is(err, saveaction.ErrStale) {
		authorizationError(w, r, err)
		return
	}
	internalServerError(w, r, "Job relationships unavailable", err)
}

func filterJobFormChoices(ctx context.Context, form templates.JobFormPageData) templates.JobFormPageData {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return form
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	if err != nil {
		return templates.JobFormPageData{Job: form.Job}
	}
	for _, entry := range []struct {
		permission string
		selected   int64
		options    *[]templates.SelectOption
	}{
		{"customer.view", form.Job.CustomerID, &form.Customers},
		{"project.view", form.Job.ProjectID, &form.Projects},
		{"location.view", form.Job.LocationID, &form.Locations},
		{"asset.view", form.Job.AssetID, &form.Assets},
	} {
		if !p.Allows(entry.permission, authorization.All) {
			*entry.options = nil
			if entry.selected > 0 {
				*entry.options = append(*entry.options, templates.SelectOption{Value: entry.selected, Label: "Retain current link"})
			}
		}
	}
	if !p.Allows("directory.view", authorization.All) {
		form.Users = nil
	}
	return form
}

func validJobRelationshipInputs(w http.ResponseWriter, r *http.Request) bool {
	for _, name := range []string{"customer_id", "project_id", "location_id", "customer_contact_id", "asset_id"} {
		values, ok := r.PostForm[name]
		if !ok {
			continue
		}
		if len(values) != 1 {
			http.Error(w, "Invalid Job relationship", 400)
			return false
		}
		if values[0] == "" && name != "customer_id" {
			continue
		}
		v, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || v < 0 || name == "customer_id" && v == 0 {
			http.Error(w, "Invalid Job relationship", 400)
			return false
		}
	}
	return true
}

func (h *JobHandler) Contacts(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid Contacts", 400)
			return
		}
		ids := []int64{}
		for _, v := range r.PostForm["contact_ids"] {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "Invalid Contacts", 400)
				return
			}
			ids = append(ids, id)
		}
		if err := req.Service.SetJobContacts(r.Context(), req.Actor, id, ids); err != nil {
			jobRelationshipError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d/contacts?flash=Contacts+saved", id), http.StatusSeeOther)
		return
	}
	choices, err := req.Service.JobContacts(r.Context(), req.Actor, id)
	if err != nil {
		jobRelationshipError(w, r, err)
		return
	}
	render(w, r, templates.JobContacts(id, choices))
}

func (h *JobHandler) Links(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid relationships", 400)
			return
		}
		var in authorization.JobLinks
		for name, dest := range map[string]*int64{"project_id": &in.ProjectID, "location_id": &in.LocationID, "asset_id": &in.AssetID} {
			v, err := strconv.ParseInt(r.PostForm.Get(name), 10, 64)
			if err != nil || v < 0 {
				http.Error(w, "Invalid relationships", 400)
				return
			}
			*dest = v
		}
		if err := req.Service.SetJobLinks(r.Context(), req.Actor, id, in); err != nil {
			jobRelationshipError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d/links?flash=Relationships+saved", id), 303)
		return
	}
	in, err := req.Service.JobLinks(r.Context(), req.Actor, id)
	if err != nil {
		jobRelationshipError(w, r, err)
		return
	}
	render(w, r, templates.JobLinks(id, in))
}

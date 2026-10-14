package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func (h *AssetHandler) Associations(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		in := authorization.AssetAssociationInput{Action: chi.URLParam(r, "action"), EffectiveDate: r.FormValue("effective_date"), Reason: r.FormValue("reason")}
		if in.Action == "correct" && !r.PostForm.Has("event_id") {
			// Missing is not the explicit origin target (0). Let the command
			// check authority before reporting an unavailable correction target.
			in.EventID = -1
		}
		for key, dest := range map[string]*int64{"customer_id": &in.CustomerID, "location_id": &in.LocationID, "event_id": &in.EventID, "revision": &in.Revision} {
			value := r.FormValue(key)
			if value == "" && key != "revision" {
				continue
			}
			*dest, err = strconv.ParseInt(value, 10, 64)
			if err != nil || *dest < 0 {
				http.Error(w, "Invalid association selection or revision", 400)
				return
			}
		}
		if err = req.Service.ChangeAssetAssociation(r.Context(), req.Actor, id, in); err != nil {
			writeAssetVocabularyError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/assets/%d/associations?flash=Association+saved", id), 303)
		return
	}
	history, err := req.Service.AssetAssociationHistory(r.Context(), req.Actor, id)
	if err != nil {
		writeAssetVocabularyError(w, r, err)
		return
	}
	p, err := req.Service.Principal(r.Context(), req.Actor)
	if err != nil {
		writeAssetVocabularyError(w, r, err)
		return
	}
	actions := []string{}
	for _, action := range []string{"transfer", "place", "return", "correct"} {
		permission := "asset." + action
		if action == "correct" {
			permission = "asset.history.correct"
		}
		if p.Allows(permission, authorization.All) {
			actions = append(actions, action)
		}
	}
	var customers []templates.SelectOption
	if p.Allows("customer.view", authorization.All) {
		rows, err := h.customerSvc.ListAll(r.Context())
		if err != nil {
			writeAssetVocabularyError(w, r, err)
			return
		}
		customers = customersToOptions(rows)
	}
	templates.AssetAssociations(id, history, actions, customers).Render(r.Context(), w)
}

func (h *AssetHandler) CustomerHistory(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	periods, err := req.Service.CustomerAssetHistory(r.Context(), req.Actor, id)
	if err != nil {
		writeAssetVocabularyError(w, r, err)
		return
	}
	render(w, r, templates.CustomerAssetHistory(id, periods))
}

package handlers

import (
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func (h *JobHandler) Assets(w http.ResponseWriter, r *http.Request) {
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
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		unlink := r.FormValue("action") == "unlink"
		if !unlink && r.FormValue("action") != "link" {
			http.Error(w, "Invalid action", 400)
			return
		}
		key := "asset_id"
		if unlink {
			key = "link_id"
		}
		target, err := strconv.ParseInt(r.FormValue(key), 10, 64)
		if err != nil || target <= 0 {
			http.Error(w, "Invalid Asset link", 400)
			return
		}
		assetID, linkID := target, int64(0)
		if unlink {
			assetID, linkID = 0, target
		}
		if err = req.Service.ChangeJobAsset(r.Context(), req.Actor, id, assetID, linkID, unlink); err != nil {
			jobRelationshipError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d/assets?flash=Asset+links+saved", id), 303)
		return
	}
	choices, err := req.Service.JobLinks(r.Context(), req.Actor, id)
	if err != nil {
		jobRelationshipError(w, r, err)
		return
	}
	links, err := req.Service.LinkedJobAssets(r.Context(), req.Actor, id)
	if err != nil {
		jobRelationshipError(w, r, err)
		return
	}
	render(w, r, templates.JobAssets(id, choices.Assets, links))
}

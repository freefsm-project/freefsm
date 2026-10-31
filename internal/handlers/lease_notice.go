package handlers

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func (h *LeaseHandler) Notice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var values url.Values
	message := ""
	conflict := false
	if r.Method == http.MethodPost {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		values = r.PostForm
		if values.Get("recovery") != "revise" {
			revision, parseErr := strconv.ParseInt(values.Get("expected_revision"), 10, 64)
			if parseErr != nil {
				message = "Invalid revision. Compare current facts before revising."
				conflict = true
			} else {
				in := authorization.LeaseNoticeInput{ExpectedRevision: revision, Action: values.Get("action"), Category: values.Get("category"), Initiator: values.Get("initiator"), NoticeOn: values.Get("notice_on"), EffectiveOn: values.Get("effective_on"), Reason: values.Get("reason"), OperationKey: values.Get("operation_key")}
				err = h.authority.ChangeLeaseNotice(r.Context(), authorizationActor(r), id, in)
				if err == nil {
					http.Redirect(w, r, fmt.Sprintf("/leases/%d/notice", id), http.StatusSeeOther)
					return
				}
				var validation *authorization.LeaseValidationError
				switch {
				case errors.As(err, &validation):
					message = validation.Message
				case errors.Is(err, authorization.ErrConflict):
					message = "Notice was not saved. Compare current facts, then revise or discard your proposal."
					conflict = true
				default:
					authorizationError(w, r, err)
					return
				}
			}
		}
	}
	// Current visibility/management is required before reflecting retained input.
	data, err := h.authority.ReviewLeaseNotice(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if values != nil && !data.CanManage {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if values == nil {
		values = url.Values{"expected_revision": {fmt.Sprint(data.Revision)}, "operation_key": {rand.Text()}, "action": {"create"}, "notice_on": {data.Today}, "category": {"customer_ordinary"}, "initiator": {"customer"}}
		if len(data.History) > 0 && data.History[0].Action != "withdraw" {
			current := data.History[0]
			values.Set("action", "revise")
			values.Set("category", current.Category)
			values.Set("initiator", current.Initiator)
			values.Set("notice_on", current.NoticeOn)
			values.Set("effective_on", current.EffectiveOn)
		}
	} else if values.Get("recovery") == "revise" {
		values.Set("expected_revision", fmt.Sprint(data.Revision))
		values.Set("operation_key", rand.Text())
		message = "Current revision loaded. Review your proposal before saving."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if message != "" {
		w.WriteHeader(http.StatusConflict)
	}
	templates.LeaseNoticePage(data, values, message, conflict).Render(r.Context(), w)
}

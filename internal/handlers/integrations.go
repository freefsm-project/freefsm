package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/credentials"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type integrationHandler struct {
	authority       *authorization.Service
	email           *services.EmailService
	control         *instancecontrol.Control
	geocoderDefault string
}

func (h *integrationHandler) enableEmail(w http.ResponseWriter, r *http.Request) {
	if h.control == nil {
		http.Error(w, "Email gate unavailable", 503)
		return
	}
	ctx, release, err := h.control.Enter(r.Context())
	if err != nil {
		http.Error(w, "Instance is in maintenance", 503)
		return
	}
	defer release()
	if err := h.authority.AuthorizeEmailEnable(ctx, authorizationActor(r)); err != nil {
		authorizationError(w, r, err)
		return
	}
	if err := h.control.EnableEmail(); err != nil {
		http.Error(w, "Email re-enable could not be persisted; retry after correcting instance storage", 503)
		return
	}
	http.Redirect(w, r, "/settings/mail?flash=Outbound+email+enabled", 303)
}

func (h *integrationHandler) coordinates(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	a := authorizationActor(r)
	v, err := h.authority.LocationCoordinates(r.Context(), a, id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if r.Method == "POST" {
		if strings.HasSuffix(r.URL.Path, "/lookup") {
			saveaction.ReadOnly(r.Context()) // provider proposal only; no coordinates are persisted
			v, err = h.authority.LookupCoordinates(r.Context(), a, id, r.FormValue("revision"), h.geocoderDefault)
		} else {
			lat, e1 := strconv.ParseFloat(r.FormValue("latitude"), 64)
			lon, e2 := strconv.ParseFloat(r.FormValue("longitude"), 64)
			if e1 != nil || e2 != nil {
				saveaction.RejectBeforeWrite(r.Context())
				http.Error(w, "Enter a valid geographic pair", 400)
				return
			}
			err = h.authority.SaveCoordinates(r.Context(), a, id, r.FormValue("revision"), lat, lon)
			if err == nil {
				http.Redirect(w, r, r.URL.Path+"?flash=Coordinates+saved", 303)
				return
			}
		}
		if err != nil {
			authorizationError(w, r, err)
			return
		}
	}
	p, err := h.authority.Principal(r.Context(), a)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	render(w, r, templates.CoordinatesPage(v, !v.ReadOnly && p.Allows("location.edit", authorization.All)))
}

func (h *integrationHandler) settings(w http.ResponseWriter, r *http.Request) {
	section := strings.TrimPrefix(r.URL.Path, "/settings/")
	a := authorizationActor(r)
	v, err := h.authority.IntegrationSettings(r.Context(), a, section)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if r.Method == "POST" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		port, _ := strconv.Atoi(r.FormValue("smtp_port"))
		v = authorization.IntegrationSettings{Revision: r.FormValue("revision"), Host: r.FormValue("smtp_host"), Port: port, User: r.FormValue("smtp_user"), From: r.FormValue("smtp_from"), Copies: r.FormValue("email_auto_cc"), InvoiceSubject: r.FormValue("invoice_email_subject"), InvoiceBody: r.FormValue("invoice_email_body"), EstimateSubject: r.FormValue("estimate_email_subject"), EstimateBody: r.FormValue("estimate_email_body"), TileURL: r.FormValue("map_tile_url"), GeocoderURL: r.FormValue("geocoder_url"), Tracking: r.FormValue("tracking") == "on"}
		v.InvoiceHTML, v.EstimateHTML = r.FormValue("invoice_email_html"), r.FormValue("estimate_email_html")
		v.ClearPassword = r.FormValue("clear_password") == "on"
		if err := h.authority.SaveIntegrationSettings(r.Context(), a, section, v, r.FormValue("smtp_password")); err != nil {
			authorizationError(w, r, err)
			return
		}
		http.Redirect(w, r, r.URL.Path+"?flash=Settings+saved", 303)
		return
	}
	p, err := h.authority.Principal(r.Context(), a)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	manage := p.Allows("configuration."+section+".manage", authorization.Configuration)
	if section == "open-tracking" {
		manage = p.Allows("security.open_tracking.manage", authorization.Sealed)
	}
	render(w, r, templates.IntegrationSettingsPage(section, v, manage, p.Allows("configuration.mail.test", authorization.Configuration), h.email.EmailDisabled(), p.Allows("email.enable", authorization.Sealed)))
}

func (h *integrationHandler) testMail(w http.ResponseWriter, r *http.Request) {
	p, err := h.authority.Principal(r.Context(), authorizationActor(r))
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		authorizationError(w, r, err)
		return
	}
	if !p.Allows("configuration.mail.test", authorization.Configuration) {
		saveaction.RejectBeforeWrite(r.Context())
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if err := h.email.CheckAvailability(r.Context()); err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Outbound email is disabled; an Administrator must re-enable email", 503)
		return
	}
	recipients, err := services.ParseEmailList(r.FormValue("recipient"))
	if err != nil || len(recipients) != 1 {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Enter one recipient email address", 400)
		return
	}
	if err := h.email.ValidateConfiguration(r.Context()); err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, err.Error(), 503)
		return
	}
	if err := h.authority.TestMail(r.Context(), authorizationActor(r), func() error { return h.email.SendTestEmail(r.Context(), recipients[0], "there") }); err != nil {
		if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, credentials.ErrKey) || errors.Is(err, credentials.ErrReview) {
			authorizationError(w, r, err)
			return
		}
		http.Error(w, "Transport submission was not confirmed. Review saved configuration and provider evidence before sending again; inbox delivery is unknown.", 503)
		return
	}
	render(w, r, templates.TestEmailResult(true, "Transport accepted the test message. Inbox delivery is not confirmed."))
}

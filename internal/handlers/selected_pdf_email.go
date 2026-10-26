package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/google/uuid"
)

func selectedPDFEmail(w http.ResponseWriter, r *http.Request, kind string, id int64, queue *delivery.Service) bool {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		return false
	}
	// Validate the original query even if middleware has already parsed Form.
	// URL.Query silently drops malformed pairs and must not select a default.
	if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Invalid PDF selection", http.StatusBadRequest)
		return true
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return true
	}
	selected, present := r.Form["pdf_version"]
	alias, aliasPresent := r.Form["version"]
	if !present && r.Method == http.MethodGet {
		selected, present = alias, aliasPresent
		aliasPresent = false
	}
	if present && (len(selected) != 1 || selected[0] == "" || aliasPresent) {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Invalid PDF selection", 422)
		return true
	}
	text := ""
	if present {
		text = selected[0]
	}
	var version int64
	if text != "" {
		var err error
		version, err = strconv.ParseInt(text, 10, 64)
		if err != nil || version < 1 {
			saveaction.RejectBeforeWrite(r.Context())
			http.Error(w, "Invalid PDF selection", 422)
			return true
		}
	}
	if r.Method == http.MethodPost && version == 0 {
		saveaction.RejectBeforeWrite(r.Context())
		http.Error(w, "Select a saved PDF version before sending", 409)
		return true
	}
	selection, err := request.Service.DeliveryPDF(r.Context(), request.Actor, kind, id, version)
	if err != nil {
		saveaction.RejectBeforeWrite(r.Context()) // only selection was read; Queue has not run
		if errors.Is(err, authorization.ErrConflict) {
			http.Error(w, "Select a reviewed saved PDF version, or generate a new version before composing email", 409)
		} else {
			authorizationError(w, r, err)
		}
		return true
	}
	var source struct {
		Document struct {
			Title  string `json:"title"`
			Number string `json:"number_label"`
		}
		Customer struct {
			Name  string `json:"display_name"`
			Email string `json:"email"`
		}
		Job struct {
			Title    string `json:"job_type"`
			Subtitle string `json:"subtitle"`
		}
	}
	if err := json.Unmarshal(selection.Source, &source); err != nil {
		internalServerError(w, r, "load PDF source", err)
		return true
	}
	doc := documentPDF{Title: source.Document.Title, Number: source.Document.Number, CustomerEmail: source.Customer.Email, CustomerName: source.Customer.Name, JobName: source.Job.Title, JobType: source.Job.Title, JobSubtitle: source.Job.Subtitle, Date: displayDate(r.Context(), time.Now())}
	cs := middleware.CompanyFromContext(r.Context())
	data := templates.DocumentEmailData{ObjectType: kind, ObjectID: id, Title: doc.Title, BackURL: fmt.Sprintf("/%ss/%d/pdf/preview", kind, id), ActionURL: fmt.Sprintf("/%ss/%d/email", kind, id), To: doc.CustomerEmail, CC: emailAutoCC(cs), IdempotencyKey: uuid.NewString(), PDFVersion: selection.Version.ID, PDFStale: selection.Version.Stale}
	data.Subject, data.Body = documentEmailDefaults(kind, doc, cs)
	if r.Method == http.MethodGet {
		data.HTMLBody, err = composeHTMLDefault(queue, r, kind, doc, cs)
		if err != nil {
			internalServerError(w, r, "load email HTML template", err)
			return true
		}
		render(w, r, templates.DocumentEmailCompose(data))
		return true
	}
	data.To = r.FormValue("to")
	data.CC = r.FormValue("cc")
	data.BCC = r.FormValue("bcc")
	data.Subject = r.FormValue("subject")
	data.Body = r.FormValue("body")
	data.HTMLBody = r.FormValue("html_body")
	data.IdempotencyKey = r.FormValue("idempotency_key")
	data.ConfirmStalePDF = r.FormValue("confirm_stale_pdf") == "1"
	recipients, err := services.ParseEmailRecipients(data.To, data.CC, data.BCC)
	key, keyErr := uuid.Parse(data.IdempotencyKey)
	if err != nil || keyErr != nil || data.Subject == "" || data.Body == "" {
		saveaction.RejectBeforeWrite(r.Context())
		data.Error = "Valid recipients, subject, body and operation ID are required"
		w.WriteHeader(422)
		render(w, r, templates.DocumentEmailCompose(data))
		return true
	}
	u, _ := middleware.UserFromContext(r.Context())
	_, err = queue.Queue(r.Context(), delivery.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, delivery.QueueRequest{Key: key, Document: delivery.DocumentRef{Type: kind, ID: id}, PDFVersionID: selection.Version.ID, ConfirmStalePDF: data.ConfirmStalePDF, Snapshot: delivery.Snapshot{To: recipients.To, CC: recipients.CC, BCC: recipients.BCC, Subject: data.Subject, TextBody: data.Body, HTMLBody: selectedEmailHTML(data.Body, data.HTMLBody), PDF: selection.Version.Content, PDFFilename: selection.Version.Filename}})
	if errors.Is(err, authorization.ErrConflict) {
		data.PDFStale = true
		data.Error = "The selected PDF is stale. Confirm sending this exact version or select a new version."
		w.WriteHeader(409)
		render(w, r, templates.DocumentEmailCompose(data))
		return true
	}
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) {
		authorizationError(w, r, err)
		return true
	}
	if err != nil {
		writeDeliveryError(w, r, err, data)
		return true
	}
	http.Redirect(w, r, fmt.Sprintf("/%ss/%d?flash=Email+queued", kind, id), 303)
	return true
}

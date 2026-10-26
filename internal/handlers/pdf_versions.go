package handlers

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

// PDFRenderInput makes the reviewed presentation and financial snapshot explicit
// without changing ordinary request configuration or authorization context.
type PDFRenderInput struct {
	Asset          *ent.Asset
	FieldLines     []string
	Presentation   *ent.CompanySettings
	SettledCents   *int64
	AllocatedCents int64
	AppliedCents   int64
}

func documentPDFVersions(w http.ResponseWriter, r *http.Request, kind string, id int64, action string) bool {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		return false
	}
	switch action {
	case "preview":
		versions, err := request.Service.PDFVersions(r.Context(), request.Actor, kind, id)
		if err != nil {
			authorizationError(w, r, err)
			return true
		}
		generate := request.Service.PDFPermission(r.Context(), request.Actor, kind, id, "pdf.generate") == nil
		download := request.Service.PDFPermission(r.Context(), request.Actor, kind, id, "pdf.download") == nil
		deliver := request.Service.PDFPermission(r.Context(), request.Actor, kind, id, "delivery.request") == nil
		render(w, r, templates.DocumentPDFVersions(kind, id, versions, generate, download, deliver))
	case "save":
		source, err := request.Service.DocumentPDFSource(r.Context(), request.Actor, kind, id)
		if err != nil {
			authorizationError(w, r, err)
			return true
		}
		filename, content, err := services.RenderPDFSource(kind, source)
		if err != nil {
			internalServerError(w, r, "PDF generation failed", err)
			return true
		}
		if err := request.Service.SavePDFVersion(r.Context(), request.Actor, kind, id, source, filename, content); err != nil {
			if errors.Is(err, authorization.ErrConflict) || errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) || errors.Is(err, authorization.ErrUnavailable) {
				authorizationError(w, r, err)
			} else {
				internalServerError(w, r, "PDF storage failed", err)
			}
			return true
		}
		http.Redirect(w, r, fmt.Sprintf("/%ss/%d/pdf/preview", kind, id), 303)
	case "download":
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "invalid PDF version selection", http.StatusBadRequest)
			return true
		}
		var version int64
		if selected, present := query["version"]; present {
			var err error
			if len(selected) == 1 {
				version, err = strconv.ParseInt(selected[0], 10, 64)
			}
			if len(selected) != 1 || err != nil || version <= 0 {
				http.Error(w, "invalid PDF version selection", http.StatusBadRequest)
				return true
			}
		}
		v, err := request.Service.DownloadPDFVersion(r.Context(), request.Actor, kind, id, version, query.Get("confirm_stale") == "1")
		if err != nil {
			authorizationError(w, r, err)
			return true
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": v.Filename}))
		w.Header().Set("Cache-Control", "private, no-store")
		w.Write(v.Content)
	}
	return true
}

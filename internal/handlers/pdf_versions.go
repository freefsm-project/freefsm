package handlers

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/templates"
)

// PDFRenderInput makes the reviewed presentation and financial snapshot explicit
// without changing ordinary request configuration or authorization context.
type PDFRenderInput struct {
	Presentation *ent.CompanySettings
	SettledCents *int64
}

func documentPDFVersions(w http.ResponseWriter, r *http.Request, kind string, id int64, action string, generate func(context.Context, PDFRenderInput) (documentPDF, error)) bool {
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
		doc, err := generate(r.Context(), PDFRenderInput{Presentation: source.Settings, SettledCents: &source.SettledCents})
		if err != nil {
			http.Error(w, "PDF generation failed", 500)
			return true
		}
		if err := request.Service.SavePDFVersion(r.Context(), request.Actor, kind, id, source.Token, source.ReviewToken, doc.Filename, doc.Data); err != nil {
			authorizationError(w, r, err)
			return true
		}
		http.Redirect(w, r, fmt.Sprintf("/%ss/%d/pdf/preview", kind, id), 303)
	case "download":
		version, _ := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
		v, err := request.Service.DownloadPDFVersion(r.Context(), request.Actor, kind, id, version, r.URL.Query().Get("confirm_stale") == "1")
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

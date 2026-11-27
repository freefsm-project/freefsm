package middleware

import (
	"context"
	"errors"
	"mime"
	"net/http"

	"github.com/justinas/nosurf"
)

type financialExportFormKey struct{}

// BrowserCSRFHandler retains nosurf configuration methods while validating the
// fixed financial export forms before nosurf's PostFormValue can discard errors.
type BrowserCSRFHandler struct{ *nosurf.CSRFHandler }

func (h *BrowserCSRFHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := DecodeFinancialExportForm(r); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Invalid export filters", http.StatusBadRequest)
		return
	}
	h.CSRFHandler.ServeHTTP(w, r)
}

func isFinancialExportPost(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/reports/invoice-aging/export", "/reports/customer-financials/export", "/reports/settlements/export", "/reports/credits/export", "/reports/payments-credits/export", "/leases/report/export":
		return true
	}
	return false
}

// DecodeFinancialExportForm is also called at the handler boundary for direct
// router callers. A cached form without our success marker is untrustworthy:
// ParseForm caches partial values even on failure and cannot replay that error.
func DecodeFinancialExportForm(r *http.Request) error {
	if !isFinancialExportPost(r) {
		return nil
	}
	if valid, _ := r.Context().Value(financialExportFormKey{}).(bool); valid {
		return nil
	}
	if r.Form != nil || r.PostForm != nil {
		return errors.New("financial export form was parsed without validation")
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return errors.New("invalid financial export form encoding")
	}
	// net/http bounds URL-encoded request bodies and returns decoding/read errors.
	if err := r.ParseForm(); err != nil {
		return err
	}
	*r = *r.WithContext(context.WithValue(r.Context(), financialExportFormKey{}, true))
	return nil
}

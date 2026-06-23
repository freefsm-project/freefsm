package handlers

import (
	"bytes"
	"io"
	"net/http"
)

func writePDFResponse(w http.ResponseWriter, filename string, generate func(io.Writer) error) {
	var buf bytes.Buffer
	if err := generate(&buf); err != nil {
		http.Error(w, "PDF generation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	_, _ = buf.WriteTo(w)
}

package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Compare only retained source facts, never reconstruct history from today's
// document. This adapter supports the installed pre-format source shape while
// leaving its bytes, original fingerprint, identity and snapshot untouched.
func pdfSourceFingerprint(raw []byte) (string, bool) {
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return "", false
	}
	document, ok := source["document"].(map[string]any)
	if !ok {
		return "", false
	}
	presentation, ok := source["presentation"].(map[string]any)
	if !ok {
		return "", false
	}
	for _, key := range []string{"id", "customer_id", "job_id", "number_label", "title", "notes", "terms", "line_items", "tax_rate", "created_at", "status_id"} {
		if _, ok := document[key]; !ok {
			return "", false
		}
	}
	for _, key := range []string{"business_name", "invoice_logo_path", "invoice_color", "invoice_footer", "timezone", "date_format", "pdf_show_line_item_descriptions"} {
		if _, ok := presentation[key]; !ok {
			return "", false
		}
	}
	if path, _ := presentation["invoice_logo_path"].(string); path != "" {
		if _, captured := source["logo"]; !captured {
			return "", false
		}
	}
	// A host-root relocation is not a branding change: captured bytes are the
	// render input. Preserve the original path in history, not in equivalence.
	delete(presentation, "invoice_logo_path")
	project := func(value any, keys ...string) any {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		out := map[string]any{}
		nonnull := false
		for _, key := range keys {
			out[key] = m[key]
			if m[key] != nil {
				nonnull = true
			}
		}
		if !nonnull {
			return nil
		}
		return out
	}
	doc := project(document, "id", "customer_id", "job_id", "number_label", "invoice_number", "title", "notes", "terms", "line_items", "tax_rate", "financial", "invoice_date", "due_date", "valid_through", "created_at", "status_id").(map[string]any)
	if presentation["pdf_show_line_item_descriptions"] == false {
		if lines, ok := doc["line_items"].([]any); ok {
			for _, line := range lines {
				if fields, ok := line.(map[string]any); ok {
					delete(fields, "description")
				}
			}
		}
	}
	if doc["financial"] == nil {
		doc["financial"] = ""
	}
	tax := fmt.Sprint(doc["tax_rate"])
	if normalized, ok := new(big.Rat).SetString(strings.TrimSuffix(strings.TrimSpace(tax), "%")); ok {
		doc["tax_rate"] = normalized.RatString()
	}
	lines := []string{}
	if values, ok := source["custom_field_values"].([]any); ok {
		for _, value := range values {
			field, ok := value.(map[string]any)
			if !ok {
				return "", false
			}
			text := fmt.Sprint(field["value"])
			if field["type"] == "checkbox" {
				if text == "true" {
					text = "Yes"
				} else if text == "false" {
					text = "No"
				}
			}
			lines = append(lines, fmt.Sprint(field["name"])+": "+text)
		}
	} else if values, ok := source["custom_field_lines"].([]any); ok {
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return "", false
			}
			lines = append(lines, text)
		}
	}
	canonical := map[string]any{"document": doc, "presentation": presentation,
		"customer":           project(source["customer"], "display_name", "company_name", "email", "phone"),
		"job":                project(source["job"], "job_type", "subtitle", "start_time", "due_date"),
		"asset":              project(source["asset"], "manufacturer", "model", "serial_number"),
		"status":             project(source["status"], "id", "name", "color", "category_key"),
		"custom_field_lines": lines, "logo": source["logo"], "logo_unavailable": source["logo_unavailable"],
		"allocations": source["allocations"], "credit_applications": source["credit_applications"]}
	encoded, err := json.Marshal(pdfCanonicalNumbers(canonical))
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), true
}

func pdfCanonicalNumbers(value any) any {
	switch value := value.(type) {
	case json.Number:
		if number, ok := new(big.Rat).SetString(string(value)); ok {
			return number.RatString()
		}
	case map[string]any:
		for key, entry := range value {
			value[key] = pdfCanonicalNumbers(entry)
		}
	case []any:
		for index, entry := range value {
			value[index] = pdfCanonicalNumbers(entry)
		}
	}
	return value
}

func readPDFVersions(ctx context.Context, q queryer, p Principal, kind string, id int64, token string, version int64) ([]PDFVersion, error) {
	rows, err := q.Query(ctx, `SELECT id,generated_at,filename,source_snapshot FROM document_pdf_versions WHERE company_id=$1 AND document_type=$2 AND document_id=$3 AND ($4=0 OR id=$4) ORDER BY generated_at DESC,id DESC`, p.CompanyID, kind, id, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []PDFVersion{}
	for rows.Next() {
		var v PDFVersion
		if err := rows.Scan(&v.ID, &v.GeneratedAt, &v.Filename, &v.Source); err != nil {
			return nil, err
		}
		fingerprint, known := pdfSourceFingerprint(v.Source)
		v.SourceKnown = known
		v.Stale = !known || fingerprint != token
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func selectedPDFVersion(ctx context.Context, q queryer, p Principal, kind string, id, version int64, token string) (PDFVersion, error) {
	versions, err := readPDFVersions(ctx, q, p, kind, id, token, version)
	if err != nil {
		return PDFVersion{}, err
	}
	for _, v := range versions {
		if version != 0 || !v.Stale {
			err := q.QueryRow(ctx, `SELECT content FROM document_pdf_versions WHERE id=$1 AND company_id=$2`, v.ID, p.CompanyID).Scan(&v.Content)
			return v, err
		}
	}
	return PDFVersion{}, pgx.ErrNoRows
}

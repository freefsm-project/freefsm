package authorization

import "encoding/json"

// Two source shapes were published: the original unversioned capture and v2.
// A missing fact is not the same as a key whose value explicitly captures null.
// This only validates retained evidence; it never consults live document state.
func pdfSourceShapeKnown(kind string, source map[string]any) bool {
	if kind != "invoice" && kind != "estimate" {
		return false
	}
	versioned := false
	if format, present := source["source_format"]; present {
		number, ok := format.(json.Number)
		if !ok || number != "2" {
			return false
		}
		versioned = true
	}
	document, ok := source["document"].(map[string]any)
	if !ok {
		return false
	}
	if kind == "invoice" {
		if !pdfHasKeys(document, "invoice_date", "due_date", "invoice_number") {
			return false
		}
		for _, key := range []string{"allocations", "credit_applications"} {
			value, present := source[key]
			if !present {
				return false
			}
			if value == nil {
				continue
			} // explicitly captured absence
			entries, ok := value.([]any)
			if !ok {
				return false
			}
			for _, entry := range entries {
				fields, ok := entry.(map[string]any)
				if !ok || !pdfHasKeys(fields, "amount_cents", "reversed", "invoice_id") {
					return false
				}
				amount, ok := fields["amount_cents"].(json.Number)
				if !ok {
					return false
				}
				cents, err := amount.Int64()
				if err != nil || cents < 0 {
					return false
				}
				if _, ok := fields["reversed"].(bool); !ok {
					return false
				}
				if key == "allocations" && !pdfHasKeys(fields, "payment_id") {
					return false
				}
				if key == "credit_applications" && !pdfHasKeys(fields, "id", "credit_id") {
					return false
				}
			}
		}
	} else if !pdfHasKeys(document, "valid_through") {
		return false
	}
	for key, required := range map[string][]string{
		"customer": {"display_name", "company_name", "email", "phone"},
		"job":      {"job_type", "subtitle", "start_time", "due_date"},
		"asset":    {"manufacturer", "model", "serial_number"},
		"status":   {"id", "name", "color", "category_key"},
	} {
		value, present := source[key]
		if !present {
			return false
		}
		if value == nil {
			continue
		}
		fields, ok := value.(map[string]any)
		if !ok || !pdfHasKeys(fields, required...) {
			return false
		}
	}
	presentation, ok := source["presentation"].(map[string]any)
	if !ok || !pdfHasKeys(presentation, "business_name", "address", "city", "state", "zip", "phone", "email", "tax_id", "invoice_color", "invoice_footer", "invoice_logo_path", "pdf_show_line_item_descriptions", "timezone", "date_format", "invoice_payment_terms") {
		return false
	}
	if versioned && (!pdfHasKeys(source, "logo", "custom_field_values") || !pdfHasKeys(document, "financial")) {
		return false
	}
	if fields, present := source["custom_field_values"]; present {
		values, ok := fields.([]any)
		if !ok {
			return false
		}
		for _, entry := range values {
			field, ok := entry.(map[string]any)
			if !ok || !pdfHasKeys(field, "name", "type", "value") {
				return false
			}
			if _, ok := field["name"].(string); !ok {
				return false
			}
			if _, ok := field["type"].(string); !ok {
				return false
			}
		}
	} else if lines, present := source["custom_field_lines"]; present {
		values, ok := lines.([]any)
		if !ok {
			return false
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	// The original writer omitted custom_field_lines when no fields rendered.
	// That documented omission remains compatible, unlike missing v2 fields.
	return true
}

func pdfHasKeys(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func submittedLineItems(raw string) string {
	var values []any
	if json.Unmarshal([]byte(raw), &values) != nil {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func validDocumentDate(value string, required bool) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return !required
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func initializeMissingDocumentFields(r *http.Request, kind string, customerID, jobID int64) error {
	keys := []string{"notes", "terms", "default_line_description", "tax_rate", "valid_through"}
	if kind == "invoice" {
		keys = []string{"notes", "terms", "default_line_description", "tax_rate", "invoice_date", "due_date"}
	}
	needsDefaults := false
	for _, key := range keys {
		if !r.Form.Has(key) {
			needsDefaults = true
			break
		}
	}
	if needsDefaults {
		var customer map[string]*string
		if request, ok := authorization.FromContext(r.Context()); ok {
			values, err := request.Service.DocumentDefaultInputs(r.Context(), request.Actor, kind, customerID, jobID)
			if err != nil {
				return err
			}
			customer = values
		}
		cs := middleware.CompanyFromContext(r.Context())
		if cs == nil {
			return authorization.ErrConfigurationUnavailable
		}
		defaults := services.ResolveDocumentDefaults(cs, kind, customer)
		values := map[string]string{"notes": defaults.Notes, "terms": defaults.Terms, "default_line_description": defaults.LineDescription, "tax_rate": "0"}
		if cs != nil {
			values["tax_rate"] = cs.DefaultTaxRate
		}
		today := time.Now().In(middleware.CompanyLocation(r.Context()))
		dateKey := "valid_through"
		if kind == "invoice" {
			dateKey = "due_date"
			values["invoice_date"] = today.Format("2006-01-02")
		}
		base := today
		if kind == "invoice" && r.Form.Get("invoice_date") != "" {
			parsed, err := time.ParseInLocation("2006-01-02", r.Form.Get("invoice_date"), middleware.CompanyLocation(r.Context()))
			if err == nil {
				base = parsed
			}
		}
		values[dateKey] = defaults.DateFrom(base)
		for key, value := range values {
			if !r.Form.Has(key) {
				r.Form.Set(key, value)
			}
		}
	}
	var lines []map[string]json.RawMessage
	if json.Unmarshal([]byte(r.Form.Get("line_items")), &lines) == nil {
		description, err := json.Marshal(r.Form.Get("default_line_description"))
		if err != nil {
			return err
		}
		for _, line := range lines {
			if line != nil {
				if _, present := line["description"]; !present {
					line["description"] = description
				}
			}
		}
		encoded, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		r.Form.Set("line_items", string(encoded))
	}
	return nil
}

// The form receives a fixed authorized default snapshot. No background request
// can replace an entered value when company or Customer defaults later change.
func documentDefaultChoices(ctx context.Context, kind string, customers []*ent.Customer, editing ...int64) (string, error) {
	today := time.Now().In(middleware.CompanyLocation(ctx))
	cs := middleware.CompanyFromContext(ctx)
	project := func(overrides map[string]*string) map[string]string {
		defaults := services.ResolveDocumentDefaults(cs, kind, overrides)
		values := map[string]string{"notes": defaults.Notes, "terms": defaults.Terms, "default_line_description": defaults.LineDescription}
		if kind == "invoice" {
			values["due_date"] = defaults.DateFrom(today)
		} else {
			values["valid_through"] = defaults.DateFrom(today)
		}
		return values
	}
	choices := map[string]map[string]string{"": project(nil)}
	if request, ok := authorization.FromContext(ctx); ok {
		ids := make([]int64, 0, len(customers))
		for _, customer := range customers {
			ids = append(ids, customer.ID)
		}
		available, err := request.Service.DocumentDefaultChoices(ctx, request.Actor, kind, ids, editing...)
		if err != nil {
			return "", err
		}
		for _, customer := range customers {
			choices[strconv.FormatInt(customer.ID, 10)] = project(available[customer.ID])
		}
	}
	encoded, err := json.Marshal(choices)
	return string(encoded), err
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/a-h/templ"
)

func render(w http.ResponseWriter, r *http.Request, component templ.Component) {
	var buf bytes.Buffer
	if err := component.Render(r.Context(), &buf); err != nil {
		slog.Error("render template", "path", r.URL.Path, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	_, _ = buf.WriteTo(w)
}

type inlineOptionResponse struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

func writeInlineOptionJSON(w http.ResponseWriter, id int64, label string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(inlineOptionResponse{ID: id, Label: label})
}

func internalServerError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Error(msg, "path", r.URL.Path, "error", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func customerMap(customers []*ent.Customer) map[int64]string {
	m := make(map[int64]string, len(customers))
	for _, c := range customers {
		m[c.ID] = c.DisplayName
	}
	return m
}

func statusOptions(statuses []*ent.Status) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(statuses))
	for i, s := range statuses {
		opts[i] = templates.SelectOption{Value: s.ID, Label: s.Name}
	}
	return opts
}

func customerOptions(customers []*ent.Customer) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(customers))
	for i, c := range customers {
		opts[i] = templates.SelectOption{Value: c.ID, Label: c.DisplayName}
	}
	return opts
}

func companyDefaultTaxRate(ctx context.Context) string {
	if cs := middleware.CompanyFromContext(ctx); cs != nil && cs.DefaultTaxRate != "" {
		return cs.DefaultTaxRate
	}
	return "0"
}

func taxRateForCustomer(ctx context.Context, custSvc *services.CustomerService, customerID int64, defaultTaxRate string) string {
	if customerID <= 0 {
		return defaultTaxRate
	}
	c, err := custSvc.GetByID(ctx, customerID)
	if err == nil && c != nil && c.TaxExempt {
		return "0"
	}
	return defaultTaxRate
}

func customerTaxContextJSON(customers []*ent.Customer, defaultTaxRate string) string {
	type customerTaxInfo struct {
		TaxExempt bool `json:"tax_exempt"`
	}
	payload := struct {
		DefaultTaxRate string                    `json:"default_tax_rate"`
		Customers      map[int64]customerTaxInfo `json:"customers"`
	}{
		DefaultTaxRate: defaultTaxRate,
		Customers:      make(map[int64]customerTaxInfo, len(customers)),
	}
	for _, c := range customers {
		payload.Customers[c.ID] = customerTaxInfo{TaxExempt: c.TaxExempt}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return `{"default_tax_rate":"0","customers":{}}`
	}
	return string(b)
}

func statusName(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return ""
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.Name
		}
	}
	return "Unknown"
}

func statusMap(statuses []*ent.Status) map[int64]string {
	m := make(map[int64]string, len(statuses))
	for _, s := range statuses {
		m[s.ID] = s.Name
	}
	return m
}

func statusColor(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return "#6B7280"
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.Color
		}
	}
	return "#6B7280"
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func formPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func parseOptionalPositiveInt64(v, label string) (*int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	return parseRequiredPositiveInt64(v, label)
}

func parseRequiredPositiveInt64(v, label string) (*int64, error) {
	v = strings.TrimSpace(v)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("%s must be a positive number", label)
	}
	return &n, nil
}

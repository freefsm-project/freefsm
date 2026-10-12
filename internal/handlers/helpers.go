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

	"github.com/a-h/templ"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
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

func objectDisplayName(ctx context.Context, objects objectref.Directory, ref objectref.Ref) string {
	name, err := objects.DisplayName(ctx, ref)
	if err == nil && name != "" {
		return name
	}
	return fmt.Sprintf("%s #%d", ref.Type, ref.ID)
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

func invoiceManualStatusOptions(statuses []*ent.Status) []templates.SelectOption {
	manual := make([]*ent.Status, 0, 4)
	for _, status := range statuses {
		switch status.CategoryKey {
		case "invoice:draft", "invoice:invoiced", "invoice:sent", "invoice:void":
			manual = append(manual, status)
		}
	}
	return statusOptions(manual)
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

func taxRateForCustomer(ctx context.Context, custSvc *services.CustomerService, customerID int64, defaultTaxRate string, jobIDs ...int64) (string, error) {
	if customerID <= 0 {
		return defaultTaxRate, nil
	}
	if request, ok := authorization.FromContext(ctx); ok {
		var jobID int64
		if len(jobIDs) > 0 {
			jobID = jobIDs[0]
		}
		input, err := request.Service.DocumentCustomer(ctx, request.Actor, customerID, jobID, defaultTaxRate)
		return input.TaxRate, err
	}
	c, err := custSvc.GetByID(ctx, customerID)
	if err == nil && c != nil && c.TaxExempt {
		return "0", nil
	}
	return defaultTaxRate, nil
}

func documentCustomerOptions(ctx context.Context, customers []*ent.Customer, customerID, jobID int64) ([]templates.SelectOption, error) {
	options := customerOptions(customers)
	if request, ok := authorization.FromContext(ctx); ok && jobID > 0 {
		for _, option := range options {
			if option.Value == customerID {
				return options, nil
			}
		}
		c, err := request.Service.JobCustomer(ctx, request.Actor, jobID)
		if err != nil {
			return nil, err
		}
		if c.ID != customerID {
			return nil, authorization.ErrNotFound
		}
		return []templates.SelectOption{{Value: c.ID, Label: c.DisplayName}}, nil
	}
	return options, nil
}

// Adapt the closed Job projection for legacy document renderers. This does not
// grant a master query or populate billing, tax, internal notes or custom fields.
func customerForDocument(ctx context.Context, svc *services.CustomerService, customerID, jobID int64) (*ent.Customer, error) {
	c, err := svc.GetByID(ctx, customerID)
	if err == nil {
		return c, nil
	}
	request, ok := authorization.FromContext(ctx)
	if !ok || jobID <= 0 {
		return nil, err
	}
	projection, err := request.Service.JobCustomer(ctx, request.Actor, jobID)
	if err != nil {
		return nil, err
	}
	if projection.ID != customerID {
		return nil, authorization.ErrNotFound
	}
	return &ent.Customer{ID: projection.ID, DisplayName: projection.DisplayName, FirstName: projection.FirstName, LastName: projection.LastName, CompanyName: projection.OrganizationName, Phone: projection.Phone, Email: projection.Email}, nil
}

type handlerCustomerTaxContext struct {
	defaultTaxRate string
	exemptions     map[int64]bool
}

func (c handlerCustomerTaxContext) DefaultTaxRate() string                { return c.defaultTaxRate }
func (c handlerCustomerTaxContext) CustomerTaxExemptions() map[int64]bool { return c.exemptions }

func lineItemEditorBootstrap(ctx context.Context, itemSvc *services.ItemService, customers []*ent.Customer, existingLineItems, defaultTaxRate string) (services.EditorBootstrap, error) {
	if request, ok := authorization.FromContext(ctx); ok {
		p, err := request.Service.Principal(ctx, request.Actor)
		if err != nil {
			return services.EditorBootstrap{}, err
		}
		if !p.Allows("customer.financial.view", authorization.All) {
			customers = nil
		}
	}
	items, err := itemSvc.ListActive(ctx)
	if err != nil {
		return services.EditorBootstrap{}, fmt.Errorf("list line item catalog: %w", err)
	}
	catalog := make([]services.CatalogSnapshot, len(items))
	for i, item := range items {
		catalog[i] = services.CatalogSnapshotFromItem(item)
	}
	exemptions := make(map[int64]bool, len(customers))
	for _, customer := range customers {
		exemptions[customer.ID] = customer.TaxExempt
	}
	return services.EncodeEditorBootstrap(catalog, existingLineItems, handlerCustomerTaxContext{
		defaultTaxRate: defaultTaxRate,
		exemptions:     exemptions,
	})
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

func statusCategory(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return ""
	}
	for _, status := range statuses {
		if status.ID == *id {
			return status.CategoryKey
		}
	}
	return ""
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func statusIDValue(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
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

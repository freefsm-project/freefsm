package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/customer"
	"github.com/MartialM1nd/freefsm/internal/ent/estimate"
	"github.com/MartialM1nd/freefsm/internal/ent/invoice"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
	"github.com/MartialM1nd/freefsm/internal/ent/project"
)

type SearchResult struct {
	ID          int64
	Type        string
	Name        string
	CustomerID  int64
	Customer    string
	StatusID    int64
	StatusName  string
	StatusColor string
	Extra       string
}

type SearchService struct {
	client *ent.Client
}

func NewSearchService(client *ent.Client) *SearchService {
	return &SearchService{client: client}
}

func (s *SearchService) Search(ctx context.Context, q string, limit int, userID int64, role string) ([]SearchResult, []SearchResult, []SearchResult, []SearchResult, []SearchResult, error) {
	canReadBilling := role == "admin" || role == "dispatcher"
	queryLimit := limit
	if !canReadBilling {
		queryLimit = limit * 5
	}

	customers, err := s.client.Customer.Query().
		Where(
			customer.DeletedAtIsNil(),
			customer.Or(
				customer.DisplayNameContainsFold(q),
				customer.FirstNameContainsFold(q),
				customer.LastNameContainsFold(q),
				customer.EmailContainsFold(q),
				customer.PhoneContains(q),
				customer.CompanyNameContainsFold(q),
			),
		).
		Limit(queryLimit).
		All(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("search customers: %w", err)
	}

	jobs, err := s.client.Job.Query().
		Where(
			job.DeletedAtIsNil(),
			job.Or(
				job.JobTypeContainsFold(q),
				job.SubtitleContainsFold(q),
				job.NotesContainsFold(q),
			),
		).
		Limit(queryLimit).
		All(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("search jobs: %w", err)
	}

	projects, err := s.client.Project.Query().
		Where(
			project.DeletedAtIsNil(),
			project.Or(
				project.NameContainsFold(q),
				project.DescriptionContainsFold(q),
			),
		).
		Limit(queryLimit).
		All(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("search projects: %w", err)
	}

	var invoices []*ent.Invoice
	var estimates []*ent.Estimate
	if canReadBilling {
		invoices, err = s.client.Invoice.Query().
			Where(
				invoice.DeletedAtIsNil(),
				invoice.Or(
					invoice.TitleContainsFold(q),
					invoice.NotesContainsFold(q),
				),
			).
			Limit(queryLimit).
			All(ctx)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("search invoices: %w", err)
		}

		estimates, err = s.client.Estimate.Query().
			Where(
				estimate.DeletedAtIsNil(),
				estimate.Or(
					estimate.TitleContainsFold(q),
					estimate.NotesContainsFold(q),
				),
			).
			Limit(queryLimit).
			All(ctx)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("search estimates: %w", err)
		}
	}

	custMap := make(map[int64]*ent.Customer)
	allCustIDs := make(map[int64]struct{})
	for _, j := range jobs {
		if j.CustomerID > 0 {
			allCustIDs[j.CustomerID] = struct{}{}
		}
	}
	for _, i := range invoices {
		if i.CustomerID != nil && *i.CustomerID > 0 {
			allCustIDs[*i.CustomerID] = struct{}{}
		}
	}
	for _, e := range estimates {
		if e.CustomerID != nil && *e.CustomerID > 0 {
			allCustIDs[*e.CustomerID] = struct{}{}
		}
	}
	for _, p := range projects {
		allCustIDs[p.CustomerID] = struct{}{}
	}
	if len(allCustIDs) > 0 {
		ids := make([]int64, 0, len(allCustIDs))
		for id := range allCustIDs {
			ids = append(ids, id)
		}
		custList, _ := s.client.Customer.Query().Where(customer.IDIn(ids...)).All(ctx)
		for _, c := range custList {
			custMap[c.ID] = c
		}
	}

	statuses, _ := s.client.Status.Query().All(ctx)
	statusMap := make(map[int64]*ent.Status)
	for _, s := range statuses {
		statusMap[s.ID] = s
	}

	custResults := make([]SearchResult, len(customers))
	for i, c := range customers {
		custResults[i] = SearchResult{
			ID:    c.ID,
			Type:  "customer",
			Name:  c.DisplayName,
			Extra: c.Email,
		}
	}

	jobResults := make([]SearchResult, len(jobs))
	for i, j := range jobs {
		name := j.JobType
		if j.Subtitle != "" {
			name = j.JobType + " — " + j.Subtitle
		}
		var custName string
		if c, ok := custMap[j.CustomerID]; ok {
			custName = c.DisplayName
		}
		var stName, stColor string
		if j.StatusID != nil {
			if s, ok := statusMap[*j.StatusID]; ok {
				stName = s.Name
				stColor = s.Color
			}
		}
		jobResults[i] = SearchResult{
			ID:         j.ID,
			Type:       "job",
			Name:       name,
			CustomerID: j.CustomerID,
			Customer:   custName,
			StatusID: func() int64 {
				if j.StatusID != nil {
					return *j.StatusID
				}
				return 0
			}(),
			StatusName:  stName,
			StatusColor: stColor,
		}
	}

	projResults := make([]SearchResult, len(projects))
	for i, p := range projects {
		var custName string
		if c, ok := custMap[p.CustomerID]; ok {
			custName = c.DisplayName
		}
		var stName, stColor string
		if p.StatusID != nil {
			if s, ok := statusMap[*p.StatusID]; ok {
				stName = s.Name
				stColor = s.Color
			}
		}
		projResults[i] = SearchResult{
			ID:         p.ID,
			Type:       "project",
			Name:       p.Name,
			CustomerID: p.CustomerID,
			Customer:   custName,
			StatusID: func() int64 {
				if p.StatusID != nil {
					return *p.StatusID
				}
				return 0
			}(),
			StatusName:  stName,
			StatusColor: stColor,
			Extra:       p.Description,
		}
	}

	invResults := make([]SearchResult, len(invoices))
	for i, inv := range invoices {
		var custName string
		if inv.CustomerID != nil {
			if c, ok := custMap[*inv.CustomerID]; ok {
				custName = c.DisplayName
			}
		}
		var stName, stColor string
		if inv.StatusID != nil {
			if s, ok := statusMap[*inv.StatusID]; ok {
				stName = s.Name
				stColor = s.Color
			}
		}
		invResults[i] = SearchResult{
			ID:   inv.ID,
			Type: "invoice",
			Name: inv.Title,
			CustomerID: func() int64 {
				if inv.CustomerID != nil {
					return *inv.CustomerID
				}
				return 0
			}(),
			Customer: custName,
			StatusID: func() int64 {
				if inv.StatusID != nil {
					return *inv.StatusID
				}
				return 0
			}(),
			StatusName:  stName,
			StatusColor: stColor,
		}
	}

	estResults := make([]SearchResult, len(estimates))
	for i, e := range estimates {
		var custName string
		if e.CustomerID != nil {
			if c, ok := custMap[*e.CustomerID]; ok {
				custName = c.DisplayName
			}
		}
		var stName, stColor string
		if e.StatusID != nil {
			if s, ok := statusMap[*e.StatusID]; ok {
				stName = s.Name
				stColor = s.Color
			}
		}
		estResults[i] = SearchResult{
			ID:   e.ID,
			Type: "estimate",
			Name: e.Title,
			CustomerID: func() int64 {
				if e.CustomerID != nil {
					return *e.CustomerID
				}
				return 0
			}(),
			Customer: custName,
			StatusID: func() int64 {
				if e.StatusID != nil {
					return *e.StatusID
				}
				return 0
			}(),
			StatusName:  stName,
			StatusColor: stColor,
		}
	}

	if !canReadBilling {
		policy := NewPolicyService(s.client)
		custResults = filterSearchResults(ctx, policy, custResults, userID, role, limit)
		jobResults = filterSearchResults(ctx, policy, jobResults, userID, role, limit)
		projResults = filterSearchResults(ctx, policy, projResults, userID, role, limit)
	}

	return trimSearchResults(custResults, limit), trimSearchResults(jobResults, limit), trimSearchResults(projResults, limit), trimSearchResults(invResults, limit), trimSearchResults(estResults, limit), nil
}

func filterSearchResults(ctx context.Context, policy *PolicyService, results []SearchResult, userID int64, role string, limit int) []SearchResult {
	if userID <= 0 {
		return nil
	}
	filtered := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if policy.CanAccessObject(ctx, userID, role, result.Type, result.ID, "read") {
			filtered = append(filtered, result)
			if len(filtered) >= limit {
				break
			}
		}
	}
	return filtered
}

func trimSearchResults(results []SearchResult, limit int) []SearchResult {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	return results[:limit]
}

package services

import (
	"context"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/predicate"
	"github.com/freefsm-project/freefsm/internal/ent/project"
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
	if !canReadBilling {
		customers, jobs, projects, err := s.searchTech(ctx, q, limit, userID)
		return customers, jobs, projects, nil, nil, err
	}
	queryLimit := limit

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
		if i.CustomerID > 0 {
			allCustIDs[i.CustomerID] = struct{}{}
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

	custResults := customersToSearchResults(customers)
	jobResults := jobsToSearchResults(jobs, custMap, statusMap)
	projResults := projectsToSearchResults(projects, custMap, statusMap)

	invResults := make([]SearchResult, len(invoices))
	for i, inv := range invoices {
		var custName string
		if inv.CustomerID > 0 {
			if c, ok := custMap[inv.CustomerID]; ok {
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
			CustomerID: inv.CustomerID,
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

	return trimSearchResults(custResults, limit), trimSearchResults(jobResults, limit), trimSearchResults(projResults, limit), trimSearchResults(invResults, limit), trimSearchResults(estResults, limit), nil
}

func (s *SearchService) searchTech(ctx context.Context, q string, limit int, userID int64) ([]SearchResult, []SearchResult, []SearchResult, error) {
	if userID <= 0 {
		return nil, nil, nil, nil
	}

	assignedJobIDs, err := NewJobService(s.client).assignedJobIDs(ctx, userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list assigned jobs for search: %w", err)
	}

	assignedJobs := make([]*ent.Job, 0)
	if len(assignedJobIDs) > 0 {
		assignedJobs, err = s.client.Job.Query().Where(job.DeletedAtIsNil(), job.IDIn(assignedJobIDs...)).All(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load assigned jobs for search: %w", err)
		}
	}

	jobCustomerIDs := make([]int64, 0, len(assignedJobs))
	jobProjectIDs := make([]int64, 0, len(assignedJobs))
	seenJobCustomers := make(map[int64]struct{}, len(assignedJobs))
	seenJobProjects := make(map[int64]struct{}, len(assignedJobs))
	for _, j := range assignedJobs {
		if j.CustomerID > 0 {
			if _, ok := seenJobCustomers[j.CustomerID]; !ok {
				seenJobCustomers[j.CustomerID] = struct{}{}
				jobCustomerIDs = append(jobCustomerIDs, j.CustomerID)
			}
		}
		if j.ProjectID != nil && *j.ProjectID > 0 {
			if _, ok := seenJobProjects[*j.ProjectID]; !ok {
				seenJobProjects[*j.ProjectID] = struct{}{}
				jobProjectIDs = append(jobProjectIDs, *j.ProjectID)
			}
		}
	}

	directCustomerIDs, err := s.client.Customer.Query().Where(customer.DeletedAtIsNil(), customer.AssignedToEQ(userID)).IDs(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list assigned customers for search: %w", err)
	}

	customers, err := s.searchTechCustomers(ctx, q, limit, userID, jobCustomerIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	jobs, err := s.searchTechJobs(ctx, q, limit, assignedJobIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	projects, err := s.searchTechProjects(ctx, q, limit, directCustomerIDs, jobProjectIDs)
	if err != nil {
		return nil, nil, nil, err
	}

	custMap := make(map[int64]*ent.Customer)
	allCustIDs := make(map[int64]struct{})
	for _, j := range jobs {
		if j.CustomerID > 0 {
			allCustIDs[j.CustomerID] = struct{}{}
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
		custList, _ := s.client.Customer.Query().Where(customer.DeletedAtIsNil(), customer.IDIn(ids...)).All(ctx)
		for _, c := range custList {
			custMap[c.ID] = c
		}
	}

	statuses, _ := s.client.Status.Query().All(ctx)
	statusMap := make(map[int64]*ent.Status)
	for _, st := range statuses {
		statusMap[st.ID] = st
	}

	return trimSearchResults(customersToSearchResults(customers), limit), trimSearchResults(jobsToSearchResults(jobs, custMap, statusMap), limit), trimSearchResults(projectsToSearchResults(projects, custMap, statusMap), limit), nil
}

func (s *SearchService) searchTechCustomers(ctx context.Context, q string, limit int, userID int64, jobCustomerIDs []int64) ([]*ent.Customer, error) {
	access := []predicate.Customer{customer.AssignedToEQ(userID)}
	if len(jobCustomerIDs) > 0 {
		access = append(access, customer.IDIn(jobCustomerIDs...))
	}
	customers, err := s.client.Customer.Query().
		Where(
			customer.DeletedAtIsNil(),
			customer.Or(access...),
			customer.Or(
				customer.DisplayNameContainsFold(q),
				customer.FirstNameContainsFold(q),
				customer.LastNameContainsFold(q),
				customer.EmailContainsFold(q),
				customer.PhoneContains(q),
				customer.CompanyNameContainsFold(q),
			),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("search customers: %w", err)
	}
	return customers, nil
}

func (s *SearchService) searchTechJobs(ctx context.Context, q string, limit int, assignedJobIDs []int64) ([]*ent.Job, error) {
	if len(assignedJobIDs) == 0 {
		return nil, nil
	}
	jobs, err := s.client.Job.Query().
		Where(
			job.DeletedAtIsNil(),
			job.IDIn(assignedJobIDs...),
			job.Or(
				job.JobTypeContainsFold(q),
				job.SubtitleContainsFold(q),
				job.NotesContainsFold(q),
			),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("search jobs: %w", err)
	}
	return jobs, nil
}

func (s *SearchService) searchTechProjects(ctx context.Context, q string, limit int, directCustomerIDs []int64, jobProjectIDs []int64) ([]*ent.Project, error) {
	access := make([]predicate.Project, 0, 2)
	if len(directCustomerIDs) > 0 {
		access = append(access, project.CustomerIDIn(directCustomerIDs...))
	}
	if len(jobProjectIDs) > 0 {
		access = append(access, project.IDIn(jobProjectIDs...))
	}
	if len(access) == 0 {
		return nil, nil
	}
	projects, err := s.client.Project.Query().
		Where(
			project.DeletedAtIsNil(),
			project.Or(access...),
			project.Or(
				project.NameContainsFold(q),
				project.DescriptionContainsFold(q),
			),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("search projects: %w", err)
	}
	return projects, nil
}

func customersToSearchResults(customers []*ent.Customer) []SearchResult {
	results := make([]SearchResult, len(customers))
	for i, c := range customers {
		results[i] = SearchResult{
			ID:    c.ID,
			Type:  "customer",
			Name:  c.DisplayName,
			Extra: c.Email,
		}
	}
	return results
}

func jobsToSearchResults(jobs []*ent.Job, custMap map[int64]*ent.Customer, statusMap map[int64]*ent.Status) []SearchResult {
	results := make([]SearchResult, len(jobs))
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
			if st, ok := statusMap[*j.StatusID]; ok {
				stName = st.Name
				stColor = st.Color
			}
		}
		results[i] = SearchResult{
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
	return results
}

func projectsToSearchResults(projects []*ent.Project, custMap map[int64]*ent.Customer, statusMap map[int64]*ent.Status) []SearchResult {
	results := make([]SearchResult, len(projects))
	for i, p := range projects {
		var custName string
		if c, ok := custMap[p.CustomerID]; ok {
			custName = c.DisplayName
		}
		var stName, stColor string
		if p.StatusID != nil {
			if st, ok := statusMap[*p.StatusID]; ok {
				stName = st.Name
				stColor = st.Color
			}
		}
		results[i] = SearchResult{
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
	return results
}

func trimSearchResults(results []SearchResult, limit int) []SearchResult {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	return results[:limit]
}

package services

import (
	"context"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/customer"
	"github.com/MartialM1nd/freefsm/internal/ent/invoice"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
	"github.com/MartialM1nd/freefsm/internal/ent/project"
	"github.com/MartialM1nd/freefsm/internal/ent/status"
	"github.com/MartialM1nd/freefsm/internal/ent/statusworkflow"
)

type DashboardStats struct {
	TotalCustomers           int
	NewCustomersThisMonth    int
	TotalJobs                int
	JobsOverdue              int
	TotalEstimates           int
	TotalInvoices            int
	InvoicesPaid             int
	InvoicesUnpaid           int
	InvoicesOverdue          int
	TotalProjects            int
	ProjectsActive           int
	ProjectsCompleted        int
	RevenueMonth             float64
	OutstandingReceivables   float64
	OverdueAmount            float64
	RecentJobs               []RecentJob
	RecentInvoices           []RecentInvoice
	RecentEstimates          []RecentEstimate
}

type RecentJob struct {
	ID          int64
	DisplayName string
	Customer    string
	CreatedAt   string
}

type RecentInvoice struct {
	ID       int64
	Title    string
	Customer string
	Total    float64
	Status   string
	CreatedAt string
}

type RecentEstimate struct {
	ID       int64
	Title    string
	Customer string
	Total    float64
	CreatedAt string
}

type DashboardService struct {
	client *ent.Client
}

func NewDashboardService(client *ent.Client) *DashboardService {
	return &DashboardService{client: client}
}

func (s *DashboardService) Stats(ctx context.Context) (DashboardStats, error) {
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	// Basic counts
	totalCustomers, _ := s.client.Customer.Query().Count(ctx)
	newCustomers, _ := s.client.Customer.Query().
		Where(customer.CreatedAtGTE(startOfMonth)).
		Count(ctx)
	totalJobs, _ := s.client.Job.Query().Count(ctx)
	totalEstimates, _ := s.client.Estimate.Query().Count(ctx)
	totalInvoices, _ := s.client.Invoice.Query().Count(ctx)
	totalProjects, _ := s.client.Project.Query().Count(ctx)

	// Jobs overdue
	jobsOverdue, _ := s.client.Job.Query().
		Where(job.DueDateNotNil(), job.DueDateLT(now)).
		Count(ctx)

	// Find status IDs
	paidStatusID := s.statusIDByName(ctx, "invoice", "Paid")
	voidStatusID := s.statusIDByName(ctx, "invoice", "Void")
	draftStatusID := s.statusIDByName(ctx, "invoice", "Draft")
	inProgressStatusID := s.statusIDByName(ctx, "project", "In Progress")
	completedStatusID := s.statusIDByName(ctx, "project", "Completed")

	// Invoices
	invoicesPaid, _ := s.client.Invoice.Query().
		Where(invoice.StatusIDEQ(paidStatusID)).
		Count(ctx)

	invoicesUnpaid, _ := s.client.Invoice.Query().
		Where(invoice.StatusIDNEQ(paidStatusID)).
		Count(ctx)

	invoicesOverdue, _ := s.client.Invoice.Query().
		Where(invoice.DueDateLT(now), invoice.StatusIDNEQ(paidStatusID)).
		Count(ctx)

	// Projects
	projectsActive, _ := s.client.Project.Query().
		Where(project.StatusIDEQ(inProgressStatusID)).
		Count(ctx)

	projectsCompleted, _ := s.client.Project.Query().
		Where(project.StatusIDEQ(completedStatusID)).
		Count(ctx)

	// Revenue this month (payments received)
	monthInvoices, _ := s.client.Invoice.Query().
		Where(invoice.InvoiceDateGTE(startOfMonth)).
		All(ctx)

	var revenue float64
	for _, i := range monthInvoices {
		payments, _ := ParsePayments(i.Payments)
		for _, p := range payments {
			revenue += p.Amount
		}
	}

	// Financial totals (exclude draft, paid, and void invoices)
	allInvoices, _ := s.client.Invoice.Query().
		Where(
			invoice.StatusIDNEQ(draftStatusID),
			invoice.StatusIDNEQ(paidStatusID),
			invoice.StatusIDNEQ(voidStatusID),
		).
		All(ctx)
	var outstanding, overdue float64
	for _, i := range allInvoices {
		balance := s.invoiceBalance(i)
		if balance > 0 {
			outstanding += balance
			if !i.DueDate.IsZero() && i.DueDate.Before(now) {
				overdue += balance
			}
		}
	}

	// Recent items
	recentJobs, _ := s.client.Job.Query().
		Order(ent.Desc(job.FieldCreatedAt)).
		Limit(5).
		All(ctx)
	recentInvoices, _ := s.client.Invoice.Query().
		Order(ent.Desc(invoice.FieldCreatedAt)).
		Limit(5).
		All(ctx)
	recentEstimates, _ := s.client.Estimate.Query().
		Order(ent.Desc("created_at")).
		Limit(5).
		All(ctx)

	// Build customer map
	customers, _ := s.client.Customer.Query().All(ctx)
	custMap := make(map[int64]string, len(customers))
	for _, c := range customers {
		custMap[c.ID] = c.DisplayName
	}

	// Build status map
	statuses, _ := s.client.Status.Query().All(ctx)
	statusMap := make(map[int64]string, len(statuses))
	for _, st := range statuses {
		statusMap[st.ID] = st.Name
	}

	return DashboardStats{
		TotalCustomers:         totalCustomers,
		NewCustomersThisMonth:  newCustomers,
		TotalJobs:              totalJobs,
		JobsOverdue:            jobsOverdue,
		TotalEstimates:         totalEstimates,
		TotalInvoices:          totalInvoices,
		InvoicesPaid:           invoicesPaid,
		InvoicesUnpaid:         invoicesUnpaid,
		InvoicesOverdue:        invoicesOverdue,
		TotalProjects:          totalProjects,
		ProjectsActive:         projectsActive,
		ProjectsCompleted:      projectsCompleted,
		RevenueMonth:           revenue,
		OutstandingReceivables: outstanding,
		OverdueAmount:          overdue,
		RecentJobs:             s.toRecentJobs(recentJobs, custMap),
		RecentInvoices:         s.toRecentInvoices(recentInvoices, custMap, statusMap),
		RecentEstimates:        s.toRecentEstimates(recentEstimates, custMap),
	}, nil
}

func (s *DashboardService) statusIDByName(ctx context.Context, objectType, name string) int64 {
	st, err := s.client.Status.Query().
		Where(
			status.HasWorkflowWith(statusworkflow.ObjectTypeEQ(objectType)),
			status.NameEQ(name),
		).
		Only(ctx)
	if err != nil {
		return 0
	}
	return st.ID
}

func (s *DashboardService) invoiceSubtotal(i *ent.Invoice) float64 {
	items, _ := ParseLineItems(i.LineItems)
	var total float64
	for _, li := range items {
		total += li.UnitPrice * float64(li.Quantity)
		total -= li.Discount
		total += li.Surcharge
	}
	return total
}

func (s *DashboardService) invoiceTotal(i *ent.Invoice) float64 {
	total := s.invoiceSubtotal(i)
		if taxRate := parseTaxRate(i.TaxRate); taxRate > 0 {
		items, _ := ParseLineItems(i.LineItems)
		var taxableTotal float64
		for _, li := range items {
			if li.Taxable {
				taxableTotal += li.UnitPrice * float64(li.Quantity)
				taxableTotal -= li.Discount
				taxableTotal += li.Surcharge
			}
		}
		total += taxableTotal * taxRate / 100
	}
	return total
}

func (s *DashboardService) invoiceBalance(i *ent.Invoice) float64 {
	total := s.invoiceTotal(i)
	payments, _ := ParsePayments(i.Payments)
	var paid float64
	for _, p := range payments {
		paid += p.Amount
	}
	balance := total - paid
	if balance < 0 {
		return 0
	}
	return balance
}

func (s *DashboardService) toRecentJobs(jobs []*ent.Job, custMap map[int64]string) []RecentJob {
	result := make([]RecentJob, len(jobs))
	for i, j := range jobs {
		dn := j.JobType
		if j.Subtitle != "" {
			dn = j.JobType + " — " + j.Subtitle
		}
		result[i] = RecentJob{
			ID:          j.ID,
			DisplayName: dn,
			Customer:    custMap[j.CustomerID],
			CreatedAt:   j.CreatedAt.Format("Jan 2, 2006"),
		}
	}
	return result
}

func (s *DashboardService) toRecentInvoices(invoices []*ent.Invoice, custMap, statusMap map[int64]string) []RecentInvoice {
	result := make([]RecentInvoice, len(invoices))
	for i, inv := range invoices {
		statusName := ""
		if inv.StatusID != nil {
			statusName = statusMap[*inv.StatusID]
		}
		customerName := ""
		if inv.CustomerID != nil {
			customerName = custMap[*inv.CustomerID]
		}
		result[i] = RecentInvoice{
			ID:        inv.ID,
			Title:     inv.Title,
			Customer:  customerName,
			Total:     s.invoiceTotal(inv),
			Status:    statusName,
			CreatedAt: inv.CreatedAt.Format("Jan 2, 2006"),
		}
	}
	return result
}

func (s *DashboardService) toRecentEstimates(estimates []*ent.Estimate, custMap map[int64]string) []RecentEstimate {
	result := make([]RecentEstimate, len(estimates))
	for i, e := range estimates {
		customerName := ""
		if e.CustomerID != nil {
			customerName = custMap[*e.CustomerID]
		}
		result[i] = RecentEstimate{
			ID:        e.ID,
			Title:     e.Title,
			Customer:  customerName,
			Total:     s.estimateTotal(e),
			CreatedAt: e.CreatedAt.Format("Jan 2, 2006"),
		}
	}
	return result
}

func (s *DashboardService) estimateTotal(e *ent.Estimate) float64 {
	items, _ := ParseLineItems(e.LineItems)
	var total float64
	for _, li := range items {
		total += li.UnitPrice * float64(li.Quantity)
		total -= li.Discount
		total += li.Surcharge
	}
		if taxRate := parseTaxRate(e.TaxRate); taxRate > 0 {
		var taxableTotal float64
		for _, li := range items {
			if li.Taxable {
				taxableTotal += li.UnitPrice * float64(li.Quantity)
				taxableTotal -= li.Discount
				taxableTotal += li.Surcharge
			}
		}
		total += taxableTotal * taxRate / 100
	}
	return total
}

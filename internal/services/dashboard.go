package services

import (
	"context"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/dashboardlayout"
	"github.com/freefsm-project/freefsm/internal/ent/dashboardwidget"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/predicate"
	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/freefsm-project/freefsm/internal/ent/statusworkflow"
)

const (
	DashboardScopeUser           = "user"
	DashboardScopeCompanyDefault = "company_default"

	WidgetClock            = "clock"
	WidgetCustomers        = "customers"
	WidgetJobs             = "jobs"
	WidgetInvoices         = "invoices"
	WidgetProjects         = "projects"
	WidgetTodayOverview    = "today_overview"
	WidgetRecentActivity   = "recent_activity"
	WidgetFinancialSummary = "financial_summary"
	WidgetAssignedWork     = "assigned_work"
)

type DashboardUser struct {
	ID        int64
	Role      string
	CompanyID int64
}

type DashboardWidgetDefinition struct {
	Type        string
	Title       string
	Description string
	AdminOnly   bool
}

type DashboardWidgetView struct {
	ID         int64
	Type       string
	Title      string
	Position   int
	Hidden     bool
	Persisted  bool
	Definition DashboardWidgetDefinition
}

type DashboardStats struct {
	TotalCustomers         int
	NewCustomersThisMonth  int
	CustomerMonths         []DashboardMonthlyBar
	NewJobsToday           int
	NewQuotesToday         int
	NewInvoicesToday       int
	PaymentsCollectedToday float64
	CustomersCreatedToday  int
	JobsCompletedToday     int
	JobsScheduledToday     int
	JobsCompletedPercent   int
	TotalJobs              int
	JobsOverdue            int
	JobMonths              []DashboardMonthlyBar
	TotalEstimates         int
	TotalInvoices          int
	InvoicesPaid           int
	InvoicesUnpaid         int
	InvoicesOverdue        int
	InvoiceMonths          []DashboardMonthlyBar
	TotalProjects          int
	ProjectsActive         int
	ProjectsCompleted      int
	ProjectMonths          []DashboardMonthlyBar
	RevenueMonth           float64
	OutstandingReceivables float64
	OverdueAmount          float64
	RecentJobs             []RecentJob
	RecentInvoices         []RecentInvoice
	RecentEstimates        []RecentEstimate
}

type DashboardMonthlyBar struct {
	Label    string
	Title    string
	Total    int
	Segments []DashboardMonthlySegment
}

type DashboardMonthlySegment struct {
	Label        string
	Count        int
	PercentOfMax int
	CSSClass     string
}

type RecentJob struct {
	ID          int64
	DisplayName string
	Customer    string
	CreatedAt   string
}

type RecentInvoice struct {
	ID        int64
	Title     string
	Customer  string
	Total     float64
	Status    string
	CreatedAt string
}

type RecentEstimate struct {
	ID        int64
	Title     string
	Customer  string
	Total     float64
	CreatedAt string
}

type DashboardService struct {
	client *ent.Client
}

func NewDashboardService(client *ent.Client) *DashboardService {
	return &DashboardService{client: client}
}

func (s *DashboardService) Widgets(ctx context.Context, u DashboardUser) ([]DashboardWidgetView, error) {
	l, err := s.findUserLayout(ctx, u.ID)
	if err == nil {
		return s.widgetsForLayout(ctx, l.ID, u.Role, false)
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	if companyLayout, err := s.findCompanyDefaultLayout(ctx, u.CompanyID); err == nil {
		return s.widgetsForLayout(ctx, companyLayout.ID, u.Role, false)
	} else if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	defs := s.defaultWidgetDefinitions(u.Role)
	views := make([]DashboardWidgetView, 0, len(defs))
	for i, def := range defs {
		views = append(views, DashboardWidgetView{
			Type:       def.Type,
			Title:      def.Title,
			Position:   i,
			Definition: def,
		})
	}
	return views, nil
}

func (s *DashboardService) EditableWidgets(ctx context.Context, u DashboardUser) ([]DashboardWidgetView, error) {
	l, err := s.ensureUserLayout(ctx, u)
	if err != nil {
		return nil, err
	}
	return s.widgetsForLayout(ctx, l.ID, u.Role, false)
}

func (s *DashboardService) AvailableWidgets(ctx context.Context, u DashboardUser) ([]DashboardWidgetDefinition, error) {
	l, err := s.ensureUserLayout(ctx, u)
	if err != nil {
		return nil, err
	}
	existing, err := s.client.DashboardWidget.Query().
		Where(dashboardwidget.LayoutIDEQ(l.ID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, w := range existing {
		if !w.Hidden {
			visible[w.WidgetType] = true
		}
	}
	var available []DashboardWidgetDefinition
	for _, def := range s.allowedWidgetDefinitions(u.Role) {
		if !visible[def.Type] {
			available = append(available, def)
		}
	}
	return available, nil
}

func (s *DashboardService) AddWidget(ctx context.Context, u DashboardUser, widgetType string) error {
	def, ok := s.widgetDefinition(widgetType)
	if !ok || !s.widgetAllowed(def, u.Role) {
		return nil
	}
	l, err := s.ensureUserLayout(ctx, u)
	if err != nil {
		return err
	}
	maxPosition := -1
	widgets, err := s.client.DashboardWidget.Query().Where(dashboardwidget.LayoutIDEQ(l.ID)).All(ctx)
	if err != nil {
		return err
	}
	for _, w := range widgets {
		if w.Position > maxPosition {
			maxPosition = w.Position
		}
		if w.WidgetType == widgetType {
			_, err := s.client.DashboardWidget.UpdateOneID(w.ID).
				SetHidden(false).
				SetPosition(maxPosition + 1).
				Save(ctx)
			return err
		}
	}
	_, err = s.client.DashboardWidget.Create().
		SetLayoutID(l.ID).
		SetWidgetType(def.Type).
		SetTitle(def.Title).
		SetPosition(maxPosition + 1).
		SetConfig("{}").
		Save(ctx)
	return err
}

func (s *DashboardService) RemoveWidget(ctx context.Context, u DashboardUser, widgetID int64) error {
	l, err := s.ensureUserLayout(ctx, u)
	if err != nil {
		return err
	}
	_, err = s.client.DashboardWidget.Update().
		Where(dashboardwidget.IDEQ(widgetID), dashboardwidget.LayoutIDEQ(l.ID)).
		SetHidden(true).
		Save(ctx)
	return err
}

func (s *DashboardService) ReorderWidget(ctx context.Context, u DashboardUser, widgetID int64, direction string) error {
	l, err := s.ensureUserLayout(ctx, u)
	if err != nil {
		return err
	}
	widgets, err := s.client.DashboardWidget.Query().
		Where(dashboardwidget.LayoutIDEQ(l.ID), dashboardwidget.HiddenEQ(false)).
		Order(ent.Asc(dashboardwidget.FieldPosition), ent.Asc(dashboardwidget.FieldID)).
		All(ctx)
	if err != nil {
		return err
	}
	for i, w := range widgets {
		if w.ID != widgetID {
			continue
		}
		j := i
		if direction == "up" {
			j = i - 1
		} else if direction == "down" {
			j = i + 1
		}
		if j < 0 || j >= len(widgets) {
			return nil
		}
		other := widgets[j]
		if _, err := s.client.DashboardWidget.UpdateOneID(w.ID).SetPosition(other.Position).Save(ctx); err != nil {
			return err
		}
		_, err = s.client.DashboardWidget.UpdateOneID(other.ID).SetPosition(w.Position).Save(ctx)
		return err
	}
	return nil
}

func (s *DashboardService) ResetWidgets(ctx context.Context, u DashboardUser) error {
	l, err := s.findUserLayout(ctx, u.ID)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.client.DashboardWidget.Delete().Where(dashboardwidget.LayoutIDEQ(l.ID)).Exec(ctx); err != nil {
		return err
	}
	return s.client.DashboardLayout.DeleteOneID(l.ID).Exec(ctx)
}

func (s *DashboardService) SaveCompanyDefaultWidgets(ctx context.Context, u DashboardUser) error {
	userLayout, err := s.ensureUserLayout(ctx, u)
	if err != nil {
		return err
	}
	userWidgets, err := s.client.DashboardWidget.Query().
		Where(dashboardwidget.LayoutIDEQ(userLayout.ID)).
		Order(ent.Asc(dashboardwidget.FieldPosition), ent.Asc(dashboardwidget.FieldID)).
		All(ctx)
	if err != nil {
		return err
	}

	companyLayout, err := s.findCompanyDefaultLayout(ctx, u.CompanyID)
	if ent.IsNotFound(err) {
		create := s.client.DashboardLayout.Create().
			SetScope(DashboardScopeCompanyDefault).
			SetName("Company Dashboard").
			SetIsDefault(true)
		if u.CompanyID > 0 {
			create.SetCompanyID(u.CompanyID)
		}
		companyLayout, err = create.Save(ctx)
	}
	if err != nil {
		return err
	}
	if _, err := s.client.DashboardWidget.Delete().Where(dashboardwidget.LayoutIDEQ(companyLayout.ID)).Exec(ctx); err != nil {
		return err
	}
	for i, w := range userWidgets {
		if _, err := s.client.DashboardWidget.Create().
			SetLayoutID(companyLayout.ID).
			SetWidgetType(w.WidgetType).
			SetTitle(w.Title).
			SetPosition(i).
			SetHidden(w.Hidden).
			SetConfig(w.Config).
			Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *DashboardService) ensureUserLayout(ctx context.Context, u DashboardUser) (*ent.DashboardLayout, error) {
	l, err := s.findUserLayout(ctx, u.ID)
	if err == nil {
		return l, nil
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	create := s.client.DashboardLayout.Create().
		SetUserID(u.ID).
		SetScope(DashboardScopeUser).
		SetName("My Dashboard")
	if u.CompanyID > 0 {
		create.SetCompanyID(u.CompanyID)
	}
	l, err = create.Save(ctx)
	if err != nil {
		return nil, err
	}

	views, err := s.Widgets(ctx, DashboardUser{ID: 0, Role: u.Role, CompanyID: u.CompanyID})
	if err != nil {
		return nil, err
	}
	for i, view := range views {
		if _, err := s.client.DashboardWidget.Create().
			SetLayoutID(l.ID).
			SetWidgetType(view.Type).
			SetTitle(view.Title).
			SetPosition(i).
			SetHidden(view.Hidden).
			SetConfig("{}").
			Save(ctx); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (s *DashboardService) findUserLayout(ctx context.Context, userID int64) (*ent.DashboardLayout, error) {
	if userID == 0 {
		return nil, &ent.NotFoundError{}
	}
	return s.client.DashboardLayout.Query().
		Where(dashboardlayout.UserIDEQ(userID), dashboardlayout.ScopeEQ(DashboardScopeUser)).
		Only(ctx)
}

func (s *DashboardService) findCompanyDefaultLayout(ctx context.Context, companyID int64) (*ent.DashboardLayout, error) {
	q := s.client.DashboardLayout.Query().Where(
		dashboardlayout.ScopeEQ(DashboardScopeCompanyDefault),
		dashboardlayout.IsDefaultEQ(true),
	)
	if companyID > 0 {
		q.Where(dashboardlayout.CompanyIDEQ(companyID))
	} else {
		q.Where(dashboardlayout.CompanyIDIsNil())
	}
	return q.Only(ctx)
}

func (s *DashboardService) widgetsForLayout(ctx context.Context, layoutID int64, role string, includeHidden bool) ([]DashboardWidgetView, error) {
	q := s.client.DashboardWidget.Query().Where(dashboardwidget.LayoutIDEQ(layoutID))
	if !includeHidden {
		q.Where(dashboardwidget.HiddenEQ(false))
	}
	widgets, err := q.Order(ent.Asc(dashboardwidget.FieldPosition), ent.Asc(dashboardwidget.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]DashboardWidgetView, 0, len(widgets))
	for _, w := range widgets {
		def, ok := s.widgetDefinition(w.WidgetType)
		if !ok || !s.widgetAllowed(def, role) {
			continue
		}
		title := w.Title
		if title == "" {
			title = def.Title
		}
		views = append(views, DashboardWidgetView{
			ID:         w.ID,
			Type:       w.WidgetType,
			Title:      title,
			Position:   w.Position,
			Hidden:     w.Hidden,
			Persisted:  true,
			Definition: def,
		})
	}
	return views, nil
}

func (s *DashboardService) defaultWidgetDefinitions(role string) []DashboardWidgetDefinition {
	if role != "admin" && role != "dispatcher" {
		return []DashboardWidgetDefinition{s.mustWidgetDefinition(WidgetClock), s.mustWidgetDefinition(WidgetAssignedWork)}
	}
	return []DashboardWidgetDefinition{
		s.mustWidgetDefinition(WidgetClock),
		s.mustWidgetDefinition(WidgetCustomers),
		s.mustWidgetDefinition(WidgetJobs),
		s.mustWidgetDefinition(WidgetInvoices),
		s.mustWidgetDefinition(WidgetProjects),
		s.mustWidgetDefinition(WidgetTodayOverview),
		s.mustWidgetDefinition(WidgetRecentActivity),
		s.mustWidgetDefinition(WidgetFinancialSummary),
	}
}

func (s *DashboardService) allowedWidgetDefinitions(role string) []DashboardWidgetDefinition {
	defs := s.allWidgetDefinitions()
	allowed := make([]DashboardWidgetDefinition, 0, len(defs))
	for _, def := range defs {
		if s.widgetAllowed(def, role) {
			allowed = append(allowed, def)
		}
	}
	return allowed
}

func (s *DashboardService) widgetAllowed(def DashboardWidgetDefinition, role string) bool {
	return !def.AdminOnly || role == "admin" || role == "dispatcher"
}

func (s *DashboardService) mustWidgetDefinition(widgetType string) DashboardWidgetDefinition {
	def, _ := s.widgetDefinition(widgetType)
	return def
}

func (s *DashboardService) widgetDefinition(widgetType string) (DashboardWidgetDefinition, bool) {
	for _, def := range s.allWidgetDefinitions() {
		if def.Type == widgetType {
			return def, true
		}
	}
	return DashboardWidgetDefinition{}, false
}

func (s *DashboardService) allWidgetDefinitions() []DashboardWidgetDefinition {
	return []DashboardWidgetDefinition{
		{Type: WidgetClock, Title: "Time Clock", Description: "Clock in/out and timesheet shortcut"},
		{Type: WidgetCustomers, Title: "Customers", Description: "Customer count and monthly growth", AdminOnly: true},
		{Type: WidgetJobs, Title: "Jobs", Description: "Job count and overdue status", AdminOnly: true},
		{Type: WidgetInvoices, Title: "Invoices", Description: "Invoice count and payment status", AdminOnly: true},
		{Type: WidgetProjects, Title: "Projects", Description: "Project count and status split", AdminOnly: true},
		{Type: WidgetTodayOverview, Title: "Overview - Today", Description: "Today jobs, quotes, invoices, payments, and completion", AdminOnly: true},
		{Type: WidgetRecentActivity, Title: "Recent Activity", Description: "Recent jobs, invoices, and estimates", AdminOnly: true},
		{Type: WidgetFinancialSummary, Title: "Financial Summary", Description: "Revenue and receivables summary", AdminOnly: true},
		{Type: WidgetAssignedWork, Title: "Assigned Work", Description: "Shortcuts to jobs and schedule"},
	}
}

func (s *DashboardService) Stats(ctx context.Context, loc *time.Location, cs *ent.CompanySettings) (DashboardStats, error) {
	now := time.Now().In(loc)
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startOfTomorrow := startOfToday.AddDate(0, 0, 1)
	todayDate := now.Format("2006-01-02")
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)

	// Basic counts
	totalCustomers, _ := s.client.Customer.Query().Where(customer.DeletedAtIsNil()).Count(ctx)
	newCustomers, _ := s.client.Customer.Query().
		Where(customer.DeletedAtIsNil(), customer.CreatedAtGTE(startOfMonth)).
		Count(ctx)
	totalJobs, _ := s.client.Job.Query().Where(job.DeletedAtIsNil()).Count(ctx)
	totalEstimates, _ := s.client.Estimate.Query().Where(estimate.DeletedAtIsNil()).Count(ctx)
	totalInvoices, _ := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil()).Count(ctx)
	totalProjects, _ := s.client.Project.Query().Where(project.DeletedAtIsNil()).Count(ctx)
	newJobsToday, _ := s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.CreatedAtGTE(startOfToday), job.CreatedAtLT(startOfTomorrow)).
		Count(ctx)
	newQuotesToday, _ := s.client.Estimate.Query().
		Where(estimate.DeletedAtIsNil(), estimate.CreatedAtGTE(startOfToday), estimate.CreatedAtLT(startOfTomorrow)).
		Count(ctx)
	newInvoicesToday, _ := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil(), invoice.InvoiceDateGTE(startOfToday), invoice.InvoiceDateLT(startOfTomorrow)).
		Count(ctx)
	customersCreatedToday, _ := s.client.Customer.Query().
		Where(customer.DeletedAtIsNil(), customer.CreatedAtGTE(startOfToday), customer.CreatedAtLT(startOfTomorrow)).
		Count(ctx)

	// Find status IDs
	paidStatusID := s.statusIDByName(ctx, "invoice", "Paid")
	voidStatusID := s.statusIDByName(ctx, "invoice", "Void")
	draftStatusID := s.statusIDByName(ctx, "invoice", "Draft")
	jobCompletedStatusID := s.statusIDByName(ctx, "job", "Completed")
	inProgressStatusID := s.statusIDByName(ctx, "project", "In Progress")
	completedStatusID := s.statusIDByName(ctx, "project", "Completed")
	overdueJobPredicates := []predicate.Job{job.DeletedAtIsNil(), job.DueDateNotNil(), job.DueDateLT(now)}
	if jobCompletedStatusID != 0 {
		overdueJobPredicates = append(overdueJobPredicates, job.StatusIDNEQ(jobCompletedStatusID))
	}
	jobsOverdue, _ := s.client.Job.Query().Where(overdueJobPredicates...).Count(ctx)
	monthStarts := dashboardMonthStarts(now)
	customerMonths := s.customerMonthlyBars(ctx, loc, monthStarts)
	jobMonths := s.jobMonthlyBars(ctx, loc, monthStarts, now, jobCompletedStatusID)
	invoiceMonths := s.invoiceMonthlyBars(ctx, loc, monthStarts, now, paidStatusID, draftStatusID, voidStatusID)
	projectMonths := s.projectMonthlyBars(ctx, loc, monthStarts, inProgressStatusID, completedStatusID)

	jobsScheduledToday, _ := s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.StartTimeGTE(startOfToday), job.StartTimeLT(startOfTomorrow)).
		Count(ctx)
	jobsCompletedToday, _ := s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.StartTimeGTE(startOfToday), job.StartTimeLT(startOfTomorrow), job.StatusIDEQ(jobCompletedStatusID)).
		Count(ctx)
	jobsCompletedPercent := 0
	if jobsScheduledToday > 0 {
		jobsCompletedPercent = jobsCompletedToday * 100 / jobsScheduledToday
	}

	// Invoices
	invoicesPaid, _ := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil(), invoice.StatusIDEQ(paidStatusID)).
		Count(ctx)

	unpaidInvoicePredicates := []predicate.Invoice{invoice.DeletedAtIsNil()}
	if paidStatusID != 0 {
		unpaidInvoicePredicates = append(unpaidInvoicePredicates, invoice.StatusIDNEQ(paidStatusID))
	}
	if draftStatusID != 0 {
		unpaidInvoicePredicates = append(unpaidInvoicePredicates, invoice.StatusIDNEQ(draftStatusID))
	}
	if voidStatusID != 0 {
		unpaidInvoicePredicates = append(unpaidInvoicePredicates, invoice.StatusIDNEQ(voidStatusID))
	}
	invoicesUnpaid, _ := s.client.Invoice.Query().
		Where(unpaidInvoicePredicates...).
		Count(ctx)

	overdueInvoicePredicates := append(unpaidInvoicePredicates, invoice.DueDateLT(now))
	invoicesOverdue, _ := s.client.Invoice.Query().
		Where(overdueInvoicePredicates...).
		Count(ctx)

	// Projects
	projectsActive, _ := s.client.Project.Query().
		Where(project.DeletedAtIsNil(), project.StatusIDEQ(inProgressStatusID)).
		Count(ctx)

	projectsCompleted, _ := s.client.Project.Query().
		Where(project.DeletedAtIsNil(), project.StatusIDEQ(completedStatusID)).
		Count(ctx)

	// Revenue this month (payments received)
	monthInvoices, _ := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil(), invoice.InvoiceDateGTE(startOfMonth)).
		All(ctx)

	var revenue float64
	for _, i := range monthInvoices {
		payments, _ := ParsePayments(i.Payments)
		for _, p := range payments {
			revenue += p.Amount
		}
	}

	paymentInvoices, _ := s.client.Invoice.Query().Where(invoice.DeletedAtIsNil()).All(ctx)
	var paymentsCollectedToday float64
	for _, i := range paymentInvoices {
		payments, _ := ParsePayments(i.Payments)
		for _, p := range payments {
			if p.Date == todayDate {
				paymentsCollectedToday += p.Amount
			}
		}
	}

	// Financial totals (exclude draft, paid, and void invoices)
	allInvoices, _ := s.client.Invoice.Query().
		Where(
			invoice.DeletedAtIsNil(),
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
		Where(job.DeletedAtIsNil()).
		Order(ent.Desc(job.FieldCreatedAt)).
		Limit(5).
		All(ctx)
	recentInvoices, _ := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil()).
		Order(ent.Desc(invoice.FieldCreatedAt)).
		Limit(5).
		All(ctx)
	recentEstimates, _ := s.client.Estimate.Query().
		Where(estimate.DeletedAtIsNil()).
		Order(ent.Desc("created_at")).
		Limit(5).
		All(ctx)

	// Build customer map
	customers, _ := s.client.Customer.Query().Where(customer.DeletedAtIsNil()).All(ctx)
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
		CustomerMonths:         customerMonths,
		NewJobsToday:           newJobsToday,
		NewQuotesToday:         newQuotesToday,
		NewInvoicesToday:       newInvoicesToday,
		PaymentsCollectedToday: paymentsCollectedToday,
		CustomersCreatedToday:  customersCreatedToday,
		JobsCompletedToday:     jobsCompletedToday,
		JobsScheduledToday:     jobsScheduledToday,
		JobsCompletedPercent:   jobsCompletedPercent,
		TotalJobs:              totalJobs,
		JobsOverdue:            jobsOverdue,
		JobMonths:              jobMonths,
		TotalEstimates:         totalEstimates,
		TotalInvoices:          totalInvoices,
		InvoicesPaid:           invoicesPaid,
		InvoicesUnpaid:         invoicesUnpaid,
		InvoicesOverdue:        invoicesOverdue,
		InvoiceMonths:          invoiceMonths,
		TotalProjects:          totalProjects,
		ProjectsActive:         projectsActive,
		ProjectsCompleted:      projectsCompleted,
		ProjectMonths:          projectMonths,
		RevenueMonth:           revenue,
		OutstandingReceivables: outstanding,
		OverdueAmount:          overdue,
		RecentJobs:             s.toRecentJobs(recentJobs, custMap, loc, cs),
		RecentInvoices:         s.toRecentInvoices(recentInvoices, custMap, statusMap, loc, cs),
		RecentEstimates:        s.toRecentEstimates(recentEstimates, custMap, loc, cs),
	}, nil
}

type dashboardMonthlyCounts struct {
	Neutral int
	Red     int
	Yellow  int
	Green   int
}

func dashboardMonthStarts(now time.Time) []time.Time {
	starts := make([]time.Time, 12)
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for i := range starts {
		starts[i] = thisMonth.AddDate(0, i-11, 0)
	}
	return starts
}

func dashboardMonthIndex(t time.Time, loc *time.Location, starts []time.Time) int {
	month := time.Date(t.In(loc).Year(), t.In(loc).Month(), 1, 0, 0, 0, 0, loc)
	for i, start := range starts {
		if month.Equal(start) {
			return i
		}
	}
	return -1
}

func dashboardStatusIDEquals(id *int64, want int64) bool {
	return id != nil && want != 0 && *id == want
}

func dashboardMonthlyBars(starts []time.Time, counts []dashboardMonthlyCounts, title string, segmentOrder []string) []DashboardMonthlyBar {
	maxTotal := 0
	for _, c := range counts {
		total := c.Neutral + c.Red + c.Yellow + c.Green
		if total > maxTotal {
			maxTotal = total
		}
	}

	bars := make([]DashboardMonthlyBar, len(starts))
	for i, start := range starts {
		c := counts[i]
		bar := DashboardMonthlyBar{
			Label: start.Format("Jan"),
			Title: title + " in " + start.Format("Jan 2006"),
			Total: c.Neutral + c.Red + c.Yellow + c.Green,
		}
		for _, name := range segmentOrder {
			segment := dashboardMonthlySegment(name, c, maxTotal)
			if segment.Count > 0 {
				bar.Segments = append(bar.Segments, segment)
			}
		}
		bars[i] = bar
	}
	return bars
}

func dashboardMonthlySegment(name string, counts dashboardMonthlyCounts, maxTotal int) DashboardMonthlySegment {
	segment := DashboardMonthlySegment{Label: name}
	switch name {
	case "created":
		segment.Count = counts.Neutral
		segment.CSSClass = "dashboard-monthly-segment-neutral"
	case "overdue":
		segment.Count = counts.Red
		segment.CSSClass = "dashboard-monthly-segment-red"
	case "unpaid", "active":
		segment.Count = counts.Yellow
		segment.CSSClass = "dashboard-monthly-segment-yellow"
	case "paid", "completed":
		segment.Count = counts.Green
		segment.CSSClass = "dashboard-monthly-segment-green"
	case "other":
		segment.Count = counts.Neutral
		segment.CSSClass = "dashboard-monthly-segment-neutral"
	}
	if segment.Count > 0 && maxTotal > 0 {
		segment.PercentOfMax = segment.Count * 100 / maxTotal
		if segment.PercentOfMax == 0 {
			segment.PercentOfMax = 1
		}
	}
	return segment
}

func (s *DashboardService) customerMonthlyBars(ctx context.Context, loc *time.Location, starts []time.Time) []DashboardMonthlyBar {
	counts := make([]dashboardMonthlyCounts, len(starts))
	customers, _ := s.client.Customer.Query().
		Where(customer.DeletedAtIsNil(), customer.CreatedAtGTE(starts[0]), customer.CreatedAtLT(starts[len(starts)-1].AddDate(0, 1, 0))).
		All(ctx)
	for _, c := range customers {
		if idx := dashboardMonthIndex(c.CreatedAt, loc, starts); idx >= 0 {
			counts[idx].Neutral++
		}
	}
	return dashboardMonthlyBars(starts, counts, "Customers created", []string{"created"})
}

func (s *DashboardService) jobMonthlyBars(ctx context.Context, loc *time.Location, starts []time.Time, now time.Time, completedStatusID int64) []DashboardMonthlyBar {
	counts := make([]dashboardMonthlyCounts, len(starts))
	jobs, _ := s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.CreatedAtGTE(starts[0]), job.CreatedAtLT(starts[len(starts)-1].AddDate(0, 1, 0))).
		All(ctx)
	for _, j := range jobs {
		idx := dashboardMonthIndex(j.CreatedAt, loc, starts)
		if idx < 0 {
			continue
		}
		if j.DueDate != nil && j.DueDate.Before(now) && !dashboardStatusIDEquals(j.StatusID, completedStatusID) {
			counts[idx].Red++
		} else {
			counts[idx].Neutral++
		}
	}
	return dashboardMonthlyBars(starts, counts, "Jobs created", []string{"created", "overdue"})
}

func (s *DashboardService) invoiceMonthlyBars(ctx context.Context, loc *time.Location, starts []time.Time, now time.Time, paidStatusID, draftStatusID, voidStatusID int64) []DashboardMonthlyBar {
	counts := make([]dashboardMonthlyCounts, len(starts))
	invoices, _ := s.client.Invoice.Query().
		Where(invoice.DeletedAtIsNil(), invoice.InvoiceDateGTE(starts[0]), invoice.InvoiceDateLT(starts[len(starts)-1].AddDate(0, 1, 0))).
		All(ctx)
	for _, inv := range invoices {
		if dashboardStatusIDEquals(inv.StatusID, draftStatusID) || dashboardStatusIDEquals(inv.StatusID, voidStatusID) {
			continue
		}
		idx := dashboardMonthIndex(inv.InvoiceDate, loc, starts)
		if idx < 0 {
			continue
		}
		if dashboardStatusIDEquals(inv.StatusID, paidStatusID) {
			counts[idx].Green++
		} else if inv.DueDate.Before(now) {
			counts[idx].Red++
		} else {
			counts[idx].Yellow++
		}
	}
	return dashboardMonthlyBars(starts, counts, "Invoices", []string{"paid", "unpaid", "overdue"})
}

func (s *DashboardService) projectMonthlyBars(ctx context.Context, loc *time.Location, starts []time.Time, inProgressStatusID, completedStatusID int64) []DashboardMonthlyBar {
	counts := make([]dashboardMonthlyCounts, len(starts))
	projects, _ := s.client.Project.Query().
		Where(project.DeletedAtIsNil(), project.CreatedAtGTE(starts[0]), project.CreatedAtLT(starts[len(starts)-1].AddDate(0, 1, 0))).
		All(ctx)
	for _, p := range projects {
		idx := dashboardMonthIndex(p.CreatedAt, loc, starts)
		if idx < 0 {
			continue
		}
		switch {
		case dashboardStatusIDEquals(p.StatusID, inProgressStatusID):
			counts[idx].Yellow++
		case dashboardStatusIDEquals(p.StatusID, completedStatusID):
			counts[idx].Green++
		default:
			counts[idx].Neutral++
		}
	}
	return dashboardMonthlyBars(starts, counts, "Projects created", []string{"other", "active", "completed"})
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

func (s *DashboardService) toRecentJobs(jobs []*ent.Job, custMap map[int64]string, loc *time.Location, cs *ent.CompanySettings) []RecentJob {
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
			CreatedAt:   FormatCompanyDate(j.CreatedAt, loc, cs),
		}
	}
	return result
}

func (s *DashboardService) toRecentInvoices(invoices []*ent.Invoice, custMap, statusMap map[int64]string, loc *time.Location, cs *ent.CompanySettings) []RecentInvoice {
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
			CreatedAt: FormatCompanyDate(inv.CreatedAt, loc, cs),
		}
	}
	return result
}

func (s *DashboardService) toRecentEstimates(estimates []*ent.Estimate, custMap map[int64]string, loc *time.Location, cs *ent.CompanySettings) []RecentEstimate {
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
			CreatedAt: FormatCompanyDate(e.CreatedAt, loc, cs),
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

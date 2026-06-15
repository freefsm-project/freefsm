package templates

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
)

func getUser(ctx context.Context) *User {
	u, ok := middleware.UserFromContext(ctx)
	if !ok || u == nil {
		return nil
	}
	return &User{
		ID:    u.ID,
		Name:  u.Name,
		Email: u.Email,
		Role:  u.Role,
	}
}

func getFlash(ctx context.Context) string {
	f, _ := middleware.FlashFromContext(ctx)
	return f
}

type User struct {
	ID    int64
	Name  string
	Email string
	Role  string
}

type LoginPageData struct {
	Error string
}

type SetupPageData struct {
	Error string
}

type DashboardData struct {
	Stats services.DashboardStats
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



type CustomerListPageData struct {
	Customers     []CustomerRow
	Page          int
	PerPage       int
	Total         int
	TotalPages    int
	Search        string
	StatusFilter  string
}

type CustomerRow struct {
	ID          int64
	DisplayName string
	FirstName   string
	LastName    string
	Email       string
	Phone       string
	CompanyName string
	Status      string
	AccountType string
}

type CustomerShowPageData struct {
	Customer CustomerDetail
}

type CustomerDetail struct {
	ID              int64
	FirstName       string
	LastName        string
	DisplayName     string
	Email           string
	Phone           string
	CompanyName     string
	Notes           string
	Status          string
	AccountType     string
	BillingAddress1 string
	BillingAddress2 string
	BillingCity     string
	BillingState    string
	BillingZipCode  string
	ServiceAddress1 string
	ServiceAddress2 string
	ServiceCity     string
	ServiceState    string
	ServiceZipCode  string
}

type CustomerFormPageData struct {
	Customer      *CustomerDetail
	Errors        map[string]string
	IsNew         bool
	Statuses      []string
	AccountTypes  []string
}

type PaginationData struct {
	CurrentPage int
	TotalPages  int
	BaseURL     string
	Target      string
}

type ProjectRow struct {
	ID                   int64
	Name                 string
	Description          string
	CustomerID           int64
	CustomerName         string
	StatusID             int64
	StatusName           string
	StatusColor          string
	CompletionPercentage float64
	StartTime            string
	EndTime              string
}

type ProjectDetail struct {
	ID                   int64
	Name                 string
	Description          string
	CustomerID           int64
	CustomerName         string
	StatusID             int64
	StatusName           string
	StatusColor          string
	LocationID           int64
	LocationName         string
	CompletionPercentage float64
	StartTime            string
	EndTime              string
	Notes                string
}

type ProjectListPageData struct {
	Projects   []ProjectRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
	StatusID   int64
	Statuses   []SelectOption
}

type ProjectShowPageData struct {
	Project ProjectDetail
	Jobs    []JobRow
}

type ProjectFormPageData struct {
	Project       *ProjectDetail
	Errors        map[string]string
	IsNew         bool
	Customers     []SelectOption
	Statuses      []SelectOption
	Locations     []SelectOption
}

type ItemRow struct {
	ID          int64
	Name        string
	Type        string
	Sku         string
	UnitPrice   float64
	UnitCost    float64
	IsActive    bool
}

type ItemDetail struct {
	ID             int64
	Name           string
	Type           string
	Sku            string
	UnitPrice      float64
	UnitCost       float64
	Taxable        bool
	TaxRate        string
	TrackInventory bool
	Description    string
	IsActive       bool
}

type ItemListPageData struct {
	Items      []ItemRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
}

type ItemFormPageData struct {
	Item     *ItemDetail
	Errors   map[string]string
	IsNew    bool
	Types    []string
}

type JobRow struct {
	ID          int64
	DisplayName string
	Customer    string
	JobType     string
	StatusID    int64
	StatusName  string
	StatusColor string
	StartTime   string
	BillingType string
}

type JobDetail struct {
	ID              int64
	CustomerID      int64
	Customer        string
	ProjectID       int64
	ProjectName     string
	LocationID      int64
	LocationName    string
	ContactID       int64
	ContactName     string
	LineItems       []services.LineItem
	Visits          []services.JobVisit
	Assignments     []services.JobAssignment
	JobType         string
	Subtitle        string
	StatusID        int64
	StatusName      string
	StatusColor     string
	BillingType     string
	StartTime       string
	EndTime         string
	DueDate         string
	Notes           string
	TechNotes       string
}

type SelectOption struct {
	Value int64
	Label string
}

type JobListPageData struct {
	Jobs       []JobRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
	StatusID   int64
	Statuses   []SelectOption
}

type JobFormPageData struct {
	Job                   *JobDetail
	Errors                map[string]string
	IsNew                 bool
	Customers             []SelectOption
	Projects              []SelectOption
	Locations             []SelectOption
	Statuses              []SelectOption
	BillingTypes          []string
	ExistingVisitsJSON    string
	ExistingAssignmentsJSON string
}

func customerFormTitle(isNew bool) string {
	if isNew {
		return "New Customer"
	}
	return "Edit Customer"
}

func itemFormTitle(isNew bool) string {
	if isNew {
		return "New Item"
	}
	return "Edit Item"
}

type EstimateRow struct {
	ID          int64
	Title       string
	Customer    string
	CustomerID  int64
	StatusID    int64
	StatusName  string
	StatusColor string
	CreatedAt   string
}

type EstimateDetail struct {
	ID          int64
	CustomerID  int64
	Customer    string
	JobID       int64
	StatusID    int64
	StatusName  string
	StatusColor string
	Title       string
	Notes       string
	TaxRate     string
	LineItems   []services.LineItem
}

type EstimateListPageData struct {
	Estimates  []EstimateRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
	StatusID   int64
	Statuses   []SelectOption
}

type EstimateFormPageData struct {
	Estimate          *EstimateDetail
	Errors            map[string]string
	IsNew             bool
	Customers         []SelectOption
	Jobs              []SelectOption
	Statuses          []SelectOption
	ItemsJSON         string
	ExistingItemsJSON string
}

type InvoiceRow struct {
	ID          int64
	Title       string
	Customer    string
	CustomerID  int64
	StatusID    int64
	StatusName  string
	StatusColor string
	InvoiceDate string
	DueDate     string
}

type InvoiceDetail struct {
	ID          int64
	CustomerID  int64
	Customer    string
	JobID       int64
	StatusID    int64
	StatusName  string
	StatusColor string
	Title       string
	Notes       string
	InvoiceDate string
	DueDate     string
	TaxRate     string
	LineItems   []services.LineItem
	Payments    []services.Payment
}

type InvoiceListPageData struct {
	Invoices   []InvoiceRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
	StatusID   int64
	Statuses   []SelectOption
}

type UserRow struct {
	ID       int64
	Name     string
	Email    string
	Role     string
	IsActive bool
}

type UserListData struct {
	Users []UserRow
}

type UserDetail struct {
	User UserRow
}

type UserDetailPage struct {
	User UserRow
}

type UserFormData struct {
	User  *UserDetail
	IsNew bool
	Roles []string
}

type ForgotPasswordData struct {
	Error    string
	Success  bool
	ResetURL string
	EMailErr string
}

type ResetPasswordData struct {
	Error string
	Token string
	Valid bool
}

type SettingsPageData struct {
	Settings *ent.CompanySettings
	IsSetup  bool
}

type InvoiceFormPageData struct {
	Invoice          *InvoiceDetail
	Errors           map[string]string
	IsNew            bool
	Customers        []SelectOption
	Jobs             []SelectOption
	Statuses         []SelectOption
	ItemsJSON        string
	ExistingItemsJSON string
}

func jobFormTitle(isNew bool) string {
	if isNew {
		return "New Job"
	}
	return "Edit Job"
}

func projectFormTitle(isNew bool) string {
	if isNew {
		return "New Project"
	}
	return "Edit Project"
}

func projectFormAction(isNew bool, id int64) string {
	if isNew {
		return "/projects"
	}
	return fmt.Sprintf("/projects/%d", id)
}

func estimateFormTitle(isNew bool) string {
	if isNew {
		return "New Estimate"
	}
	return "Edit Estimate"
}

func invoiceFormTitle(isNew bool) string {
	if isNew {
		return "New Invoice"
	}
	return "Edit Invoice"
}

func lineItemTotal(li services.LineItem) float64 {
	total := li.UnitPrice * float64(li.Quantity)
	total -= li.Discount
	total += li.Surcharge
	return total
}

func lineItemsTotal(items []services.LineItem) float64 {
	var total float64
	for _, li := range items {
		total += lineItemTotal(li)
	}
	return total
}

func customerFormAction(isNew bool, id int64) string {
	if isNew {
		return "/customers"
	}
	return fmt.Sprintf("/customers/%d", id)
}

func itemFormAction(isNew bool, id int64) string {
	if isNew {
		return "/items"
	}
	return fmt.Sprintf("/items/%d", id)
}

func jobFormAction(isNew bool, id int64) string {
	if isNew {
		return "/jobs"
	}
	return fmt.Sprintf("/jobs/%d", id)
}

func estimateFormAction(isNew bool, id int64) string {
	if isNew {
		return "/estimates"
	}
	return fmt.Sprintf("/estimates/%d", id)
}

func invoiceFormAction(isNew bool, id int64) string {
	if isNew {
		return "/invoices"
	}
	return fmt.Sprintf("/invoices/%d", id)
}

func paymentsTotal(payments []services.Payment) float64 {
	var total float64
	for _, p := range payments {
		total += p.Amount
	}
	return total
}

func csrfToken(ctx context.Context) string {
	return middleware.CSRFFromContext(ctx)
}

func companyBrandName(cs *ent.CompanySettings) string {
	if cs == nil || cs.BusinessName == "" {
		return "FreeFSM"
	}
	return cs.BusinessName
}

func companyFromCtx(ctx context.Context) *ent.CompanySettings {
	cs := middleware.CompanyFromContext(ctx)
	return cs
}

func invoicePrefix(ctx context.Context) string {
	cs := middleware.CompanyFromContext(ctx)
	if cs == nil || cs.InvoicePrefix == "" {
		return "INV-"
	}
	return cs.InvoicePrefix
}

func estimatePrefix(ctx context.Context) string {
	cs := middleware.CompanyFromContext(ctx)
	if cs == nil || cs.EstimatePrefix == "" {
		return "EST-"
	}
	return cs.EstimatePrefix
}

func userFormTitle(isNew bool) string {
	if isNew { return "New User" }
	return "Edit User"
}

func userFormAction(isNew bool, id int64) string {
	if isNew { return "/users" }
	return fmt.Sprintf("/users/%d", id)
}

func customerStatusColor(status string) string {
	switch status {
	case "lead": return "#3B82F6"
	case "opportunity": return "#F59E0B"
	case "customer": return "#10B981"
	case "lost": return "#EF4444"
	case "inactive": return "#6B7280"
	default: return "#6B7280"
	}
}

func isActivePath(ctx context.Context, prefix string) bool {
	return middleware.IsActivePath(ctx, prefix)
}

func settingsButtonText(isSetup bool) string {
	if isSetup {
		return "Complete Setup"
	}
	return "Save"
}

func settingsFormAction(isSetup bool) string {
	if isSetup {
		return "/setup/company"
	}
	return "/settings"
}

func hexToRGBA(hex string, alpha float64) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 6 {
		r, _ := strconv.ParseInt(hex[0:2], 16, 64)
		g, _ := strconv.ParseInt(hex[2:4], 16, 64)
		b, _ := strconv.ParseInt(hex[4:6], 16, 64)
		return fmt.Sprintf("rgba(%d, %d, %d, %.2f)", r, g, b, alpha)
	}
	return "rgba(0, 0, 0, 0)"
}

func badgeStyle(color string) string {
	return "background:" + hexToRGBA(color, 0.15) + ";color:" + color + ";border-color:" + color
}

func today() string {
	return time.Now().Format("2006-01-02")
}

type ContactRow struct {
	ID        int64
	FirstName string
	LastName  string
	Email     string
	Phone     string
}

type CalendarJob struct {
	ID          int64
	Day         int
	Hour        int
	JobType     string
	Customer    string
	Time        string
	StatusName  string
	StatusColor string
}

type DayData struct {
	DayNum  int
	IsToday bool
	Date    string
	Jobs    []CalendarJob
}

type WeekData struct {
	Days []DayData
}

type ScheduleDay struct {
	Date    string
	DayName string
	DayNum  int
	IsToday bool
	Jobs    []CalendarJob
}

type SchedulePageData struct {
	Title     string
	Weeks     []WeekData
	Days      []ScheduleDay
	Jobs      []CalendarJob
	PrevYear  int
	PrevMonth int
	NextYear  int
	NextMonth int
	PrevDate  string
	NextDate  string
	Date      string
	IsMonth   bool
	IsWeek    bool
	IsDay     bool
}

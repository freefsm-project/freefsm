package templates

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
)

func staticAsset(path string) string {
	return path + "?v=" + url.QueryEscape(staticAssetVersion())
}

func staticAssetVersion() string {
	version := config.Commit
	if version == "" || version == "none" {
		version = config.Version
	}
	if version == "" {
		version = "dev"
	}
	return version
}

func appVersion() string {
	if config.Version == "" {
		return "dev"
	}
	return config.Version
}

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

func canManageOperational(ctx context.Context) bool {
	u := getUser(ctx)
	return u != nil && (u.Role == "admin" || u.Role == "dispatcher")
}

func canAdmin(ctx context.Context) bool {
	u := getUser(ctx)
	return u != nil && u.Role == "admin"
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
	Stats       services.DashboardStats
	ClockWidget ClockWidgetData
	Widgets     []services.DashboardWidgetView
	EditMode    bool
}

type DashboardAddWidgetData struct {
	Widgets []services.DashboardWidgetDefinition
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
	Customers    []CustomerRow
	Page         int
	PerPage      int
	Total        int
	TotalPages   int
	Search       string
	StatusFilter string
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
	Customer     CustomerDetail
	Locations    []LocationRow
	Contacts     []ContactRow
	Jobs         []JobRow
	Estimates    []EstimateRow
	Invoices     []InvoiceRow
	Financial    CustomerFinancialSummary
	Tags         []TagRow
	AllTags      []TagRow
	CustomFields []CustomFieldDisplay
	FileList     FileListPageData
}

type CustomerFinancialSummary struct {
	TotalInvoiced       float64
	TotalPaid           float64
	TotalBalance        float64
	CurrentBalance      float64
	OverdueBalance      float64
	OpenInvoiceCount    int
	OverdueInvoiceCount int
	PaidPercent         int
	OpenPercent         int
	OverduePercent      int
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
	ArchivedAt      string
}

type LocationRow struct {
	ID        int64
	Title     string
	Address1  string
	Address2  string
	City      string
	State     string
	ZipCode   string
	Notes     string
	IsPrimary bool
}

type CustomerFormPageData struct {
	Customer     *CustomerDetail
	Errors       map[string]string
	IsNew        bool
	Statuses     []string
	AccountTypes []string
	CustomFields []CustomFieldDisplay
	Locations    []LocationRow
	Contacts     []ContactRow
	Tags         []TagRow
	AllTags      []TagRow
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
	ArchivedAt           string
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
	Project      ProjectDetail
	Jobs         []JobRow
	Tags         []TagRow
	AllTags      []TagRow
	CustomFields []CustomFieldDisplay
	FileList     FileListPageData
}

type ProjectFormPageData struct {
	Project      *ProjectDetail
	Errors       map[string]string
	IsNew        bool
	Customers    []SelectOption
	Statuses     []SelectOption
	Locations    []SelectOption
	CustomFields []CustomFieldDisplay
}

type ItemRow struct {
	ID        int64
	Name      string
	Type      string
	Sku       string
	UnitPrice float64
	UnitCost  float64
	IsActive  bool
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
	ArchivedAt     string
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
	Item   *ItemDetail
	Errors map[string]string
	IsNew  bool
	Types  []string
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
	ID           int64
	CustomerID   int64
	Customer     string
	ProjectID    int64
	ProjectName  string
	LocationID   int64
	LocationName string
	ContactID    int64
	ContactName  string
	AssetID      int64
	AssetName    string
	LineItems    []services.LineItem
	Visits       []services.JobVisit
	Assignments  []services.JobAssignment
	Subtasks     []services.JobSubtask
	Tags         []TagRow
	AllTags      []TagRow
	CustomFields []CustomFieldDisplay
	JobType      string
	Subtitle     string
	StatusID     int64
	StatusName   string
	StatusColor  string
	BillingType  string
	StartTime    string
	EndTime      string
	DueDate      string
	Notes        string
	TechNotes    string
	FileList     FileListPageData
	ArchivedAt   string
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
	CustomerID int64
	Statuses   []SelectOption
}

type JobFormPageData struct {
	Job                     *JobDetail
	Errors                  map[string]string
	IsNew                   bool
	Customers               []SelectOption
	Projects                []SelectOption
	Locations               []SelectOption
	Assets                  []SelectOption
	Users                   []SelectOption
	Statuses                []SelectOption
	BillingTypes            []string
	ExistingVisitsJSON      string
	ExistingAssignmentsJSON string
	ExistingSubtasksJSON    string
	CustomFields            []CustomFieldDisplay
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
	ID           int64
	CustomerID   int64
	Customer     string
	JobID        int64
	StatusID     int64
	StatusName   string
	StatusColor  string
	Title        string
	Notes        string
	TaxRate      string
	LineItems    []services.LineItem
	Tags         []TagRow
	AllTags      []TagRow
	CustomFields []CustomFieldDisplay
	FileList     FileListPageData
	ArchivedAt   string
}

type EstimateListPageData struct {
	Estimates  []EstimateRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
	StatusID   int64
	CustomerID int64
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
	CustomFields      []CustomFieldDisplay
}

type InvoiceRow struct {
	ID          int64
	Number      int64
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
	ID           int64
	Number       int64
	CustomerID   int64
	Customer     string
	JobID        int64
	AssetID      int64
	AssetName    string
	StatusID     int64
	StatusName   string
	StatusColor  string
	Title        string
	Notes        string
	InvoiceDate  string
	DueDate      string
	TaxRate      string
	LineItems    []services.LineItem
	Payments     []services.Payment
	Tags         []TagRow
	AllTags      []TagRow
	CustomFields []CustomFieldDisplay
	FileList     FileListPageData
	ArchivedAt   string
}

type InvoiceListPageData struct {
	Invoices   []InvoiceRow
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	Search     string
	StatusID   int64
	CustomerID int64
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

type ChangePasswordData struct {
	Error          string
	MinLength      int
	RequireUpper   bool
	RequireLower   bool
	RequireDigit   bool
	RequireSpecial bool
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
	Invoice           *InvoiceDetail
	Errors            map[string]string
	IsNew             bool
	Customers         []SelectOption
	Jobs              []SelectOption
	Statuses          []SelectOption
	ItemsJSON         string
	ExistingItemsJSON string
	CustomFields      []CustomFieldDisplay
	CancelURL         string
}

type DocumentPreviewData struct {
	ObjectType  string
	ObjectID    int64
	Title       string
	BackURL     string
	PDFURL      string
	SaveURL     string
	EmailURL    string
	DownloadURL string
	Archived    bool
}

type DocumentEmailData struct {
	ObjectType string
	ObjectID   int64
	Title      string
	BackURL    string
	ActionURL  string
	To         string
	CC         string
	BCC        string
	Subject    string
	Body       string
	Error      string
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

func subtaskCompletedCount(subtasks []services.JobSubtask) int {
	var count int
	for _, st := range subtasks {
		if st.Completed {
			count++
		}
	}
	return count
}

func tagInList(tagID int64, tags []TagRow) bool {
	for _, t := range tags {
		if t.ID == tagID {
			return true
		}
	}
	return false
}

func lineItemsTotal(items []services.LineItem) float64 {
	var total float64
	for _, li := range items {
		total += lineItemTotal(li)
	}
	return total
}

func taxAmount(items []services.LineItem, taxRate string) float64 {
	tr := parseTaxRate(taxRate)
	if tr <= 0 {
		return 0
	}
	var taxableTotal float64
	for _, li := range items {
		if li.Taxable {
			taxableTotal += lineItemTotal(li)
		}
	}
	return taxableTotal * tr
}

func parseTaxRate(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	f, _ := strconv.ParseFloat(s, 64)
	return f / 100
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

func invoiceFormCancelURL(p InvoiceFormPageData) string {
	if p.CancelURL != "" {
		return p.CancelURL
	}
	return "/invoices"
}

func invoiceFormNumberValue(p InvoiceFormPageData) string {
	if p.Invoice == nil || p.Invoice.Number == 0 {
		return ""
	}
	return fmt.Sprintf("%d", p.Invoice.Number)
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

func invoiceNumber(ctx context.Context, number int64) string {
	return services.FormatInvoiceNumber(number, middleware.CompanyFromContext(ctx))
}

func estimatePrefix(ctx context.Context) string {
	cs := middleware.CompanyFromContext(ctx)
	if cs == nil || cs.EstimatePrefix == "" {
		return "EST-"
	}
	return cs.EstimatePrefix
}

func userFormTitle(isNew bool) string {
	if isNew {
		return "New User"
	}
	return "Edit User"
}

func userFormAction(isNew bool, id int64) string {
	if isNew {
		return "/users"
	}
	return fmt.Sprintf("/users/%d", id)
}

func customerStatusColor(status string) string {
	switch status {
	case "lead":
		return "#3B82F6"
	case "opportunity":
		return "#F59E0B"
	case "customer":
		return "#10B981"
	case "lost":
		return "#EF4444"
	case "inactive":
		return "#6B7280"
	default:
		return "#6B7280"
	}
}

func isActivePath(ctx context.Context, prefix string) bool {
	return middleware.IsActivePath(ctx, prefix)
}

func themeFromCtx(ctx context.Context) string {
	return middleware.ThemeFromContext(ctx)
}

func pageTitleFromPath(ctx context.Context) string {
	if t, ok := middleware.PageHeaderTitleFromContext(ctx); ok && t != "" {
		return t
	}
	path := middleware.PathFromContext(ctx)
	switch path {
	case "/":
		return "Dashboard"
	case "/schedule":
		return "Schedule"
	case "/customers":
		return "Customers"
	case "/jobs":
		return "Jobs"
	case "/projects":
		return "Projects"
	case "/estimates":
		return "Estimates"
	case "/invoices":
		return "Invoices"
	case "/items":
		return "Items"
	case "/time-entries":
		return "Timesheets"
	case "/settings":
		return "Settings"
	case "/setup", "/setup/company":
		return "Company Setup"
	case "/users":
		return "Users"
	case "/login":
		return "Login"
	case "/forgot-password":
		return "Forgot Password"
	case "/activity":
		return "Activity"
	case "/tags":
		return "Tags"
	default:
		if strings.HasPrefix(path, "/customers/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Customer"
			}
			return "Customer"
		}
		if strings.HasPrefix(path, "/jobs/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Job"
			}
			return "Job"
		}
		if strings.HasPrefix(path, "/projects/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Project"
			}
			return "Project"
		}
		if strings.HasPrefix(path, "/estimates/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Estimate"
			}
			return "Estimate"
		}
		if strings.HasPrefix(path, "/invoices/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Invoice"
			}
			return "Invoice"
		}
		if strings.HasPrefix(path, "/items/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Item"
			}
			return "Item"
		}
		if strings.HasPrefix(path, "/time-entries/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Time Entry"
			}
			return "Timesheet"
		}
		if strings.HasPrefix(path, "/users/") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit User"
			}
			return "User"
		}
		if strings.HasPrefix(path, "/reset-password") {
			return "Reset Password"
		}
		if strings.HasPrefix(path, "/settings/custom-fields") {
			if strings.HasSuffix(path, "/edit") {
				return "Edit Custom Field"
			}
			return "Custom Fields"
		}
		return "FreeFSM"
	}
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

func invoiceLogoURL(cacheBust string) string {
	if cacheBust == "" {
		return "/settings/invoice-logo"
	}
	return "/settings/invoice-logo?t=" + cacheBust
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

func today(ctx context.Context) string {
	return time.Now().In(middleware.CompanyLocation(ctx)).Format("2006-01-02")
}

func scheduleTabClass(active bool) string {
	if active {
		return "schedule-tab active"
	}
	return "schedule-tab"
}

func schedulePeriodTabClass(active bool) string {
	if active {
		return "schedule-period-tab active"
	}
	return "schedule-period-tab"
}

func customerScopedListURL(base string, customerID int64) string {
	if customerID <= 0 {
		return base
	}
	return fmt.Sprintf("%s?customer_id=%d", base, customerID)
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
	TechID      int64
	Day         int
	Hour        int
	Duration    int
	JobType     string
	Customer    string
	Time        string
	Date        string
	DateISO     string
	StatusName  string
	StatusColor string
	Lat         float64
	Lng         float64
}

type ScheduleTech struct {
	ID   int64
	Name string
}

type DispatchColumn struct {
	Tech ScheduleTech
	Jobs []CalendarJob
}

type DispatchMatrix struct {
	Period  string
	Columns []DispatchMatrixColumn
	Rows    []DispatchMatrixRow
}

type DispatchMatrixColumn struct {
	Key     string
	Label   string
	Date    string
	Hour    int
	IsToday bool
}

type DispatchMatrixRow struct {
	Tech  ScheduleTech
	Cells []DispatchMatrixCell
}

type DispatchMatrixCell struct {
	Column DispatchMatrixColumn
	Jobs   []CalendarJob
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
	Title           string
	Weeks           []WeekData
	Days            []ScheduleDay
	Jobs            []CalendarJob
	MapJobs         []CalendarJob
	UnscheduledJobs []CalendarJob
	DispatchColumns []DispatchColumn
	DispatchMatrix  DispatchMatrix
	Techs           []ScheduleTech
	Tab             string
	Period          string
	TileURL         string
	PrevYear        int
	PrevMonth       int
	NextYear        int
	NextMonth       int
	PrevDate        string
	NextDate        string
	Date            string
	StartDate       string
	EndDate         string
	IsMonth         bool
	IsWeek          bool
	IsDay           bool
	IsList          bool
	IsDispatch      bool
	IsMap           bool
}

type TagRow struct {
	ID    int64
	Name  string
	Color string
}

type TagListPageData struct {
	Tags []TagRow
}

type TagFormData struct {
	Tag    TagRow
	IsNew  bool
	Errors map[string]string
}

type TagWidgetData struct {
	BaseURL  string
	Tags     []TagRow
	AllTags  []TagRow
	ReadOnly bool
}

type CommentRow struct {
	ID        int64
	Author    string
	Content   string
	CreatedAt string
	CanDelete bool
}

type CommentsWidgetData struct {
	BaseURL    string
	ObjectType string
	ObjectID   int64
	Comments   []CommentRow
	ReadOnly   bool
	HideTitle  bool
}

type CustomFieldDefRow struct {
	ID         int64
	ObjectType string
	Name       string
	FieldType  string
	Required   bool
	Options    string
	SortOrder  int
}

type CustomFieldDefFormData struct {
	Def         CustomFieldDefRow
	IsNew       bool
	Errors      map[string]string
	ObjectTypes []string
	FieldTypes  []string
}

type CustomFieldDefListPageData struct {
	Definitions []CustomFieldDefRow
}

type CustomFieldDisplay struct {
	DefinitionID int64
	Name         string
	FieldType    string
	Value        string
	Options      []string
	Required     bool
}

type CustomFieldsWidgetData struct {
	Fields   []CustomFieldDisplay
	EditMode bool
}

type SearchPageData struct {
	Query     string
	Customers []services.SearchResult
	Jobs      []services.SearchResult
	Projects  []services.SearchResult
	Invoices  []services.SearchResult
	Estimates []services.SearchResult
}

type ClockWidgetData struct {
	IsClockedIn bool
	Duration    string
	ClockInTime string
}

type TimeEntryRow struct {
	ID       int64
	UserName string
	IsManual bool
	ClockIn  string
	ClockOut string
	Duration string
	Notes    string
	CanEdit  bool
}

type TimeEntryListPageData struct {
	Entries        []TimeEntryRow
	Page           int
	PerPage        int
	Total          int
	TotalPages     int
	Search         string
	UserID         int64
	DateFrom       string
	DateTo         string
	ShowUserFilter bool
	Users          []UserRow
}

type TimeEntryFormEntry struct {
	ID       int64
	ClockIn  string
	ClockOut string
	Notes    string
}

type TimeEntryFormPageData struct {
	Entry  *TimeEntryFormEntry
	Errors map[string]string
}

type TimeEntryShowPageData struct {
	ID       int64
	UserName string
	ClockIn  string
	ClockOut string
	Duration string
	IsManual bool
	Notes    string
	GPSLat   string
	GPSLon   string
}

func timeEntryFormTitle() string {
	return "Edit Time Entry"
}

func timeEntryFormAction(id int64) string {
	return fmt.Sprintf("/time-entries/%d", id)
}

func companyTimeLocation(ctx context.Context) *time.Location {
	return middleware.CompanyLocation(ctx)
}

func commonTimezones() []string {
	return []string{
		"UTC",
		"America/New_York",
		"America/Chicago",
		"America/Denver",
		"America/Phoenix",
		"America/Los_Angeles",
		"America/Anchorage",
		"Pacific/Honolulu",
		"America/Toronto",
		"America/Vancouver",
		"America/Edmonton",
		"America/Winnipeg",
		"America/Halifax",
		"America/St_Johns",
		"America/Mexico_City",
		"America/Puerto_Rico",
		"America/Sao_Paulo",
		"America/Argentina/Buenos_Aires",
		"Europe/London",
		"Europe/Paris",
		"Europe/Berlin",
		"Europe/Madrid",
		"Europe/Rome",
		"Europe/Amsterdam",
		"Europe/Stockholm",
		"Europe/Moscow",
		"Europe/Istanbul",
		"Asia/Dubai",
		"Asia/Kolkata",
		"Asia/Bangkok",
		"Asia/Singapore",
		"Asia/Shanghai",
		"Asia/Tokyo",
		"Asia/Seoul",
		"Australia/Sydney",
		"Australia/Melbourne",
		"Australia/Perth",
		"Pacific/Auckland",
		"Pacific/Fiji",
	}
}

// Asset types
type AssetListPageData struct {
	Assets        []AssetRow
	AssetTypes    []SelectOption
	AssetStatuses []SelectOption
	Page          int
	PerPage       int
	Total         int
	TotalPages    int
	Search        string
	CustomerID    int64
	AssetTypeID   int64
	AssetStatusID int64
}

type AssetRow struct {
	ID            int64
	Name          string
	SerialNumber  string
	Model         string
	Manufacturer  string
	CustomerID    int64
	LocationID    *int64
	AssetTypeID   int64
	AssetStatusID *int64
}

type AssetShowPageData struct {
	Asset          AssetDetail
	ServiceHistory []JobRow
	Tags           []TagRow
	AllTags        []TagRow
	CustomFields   []CustomFieldDisplay
	FileList       FileListPageData
}

type AssetDetail struct {
	ID               int64
	CustomerID       int64
	LocationID       *int64
	AssetTypeID      int64
	AssetStatusID    *int64
	Name             string
	SerialNumber     string
	Model            string
	Manufacturer     string
	Notes            string
	InstalledAt      *time.Time
	WarrantyExpires  *time.Time
	CustomFields     string
	AssetTypeName    string
	AssetStatusName  string
	AssetStatusColor string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ArchivedAt       string
}

type AssetFormPageData struct {
	IsNew         bool
	Asset         *AssetDetail
	Customers     []SelectOption
	Locations     []SelectOption
	AssetTypes    []SelectOption
	AssetStatuses []SelectOption
	Errors        map[string]string
}

type AssetTypeListPageData struct {
	Types []AssetTypeRow
}

type AssetTypeRow struct {
	ID        int64
	Name      string
	SortOrder int
}

type AssetStatusListPageData struct {
	Statuses []AssetStatusRow
}

type AssetStatusRow struct {
	ID        int64
	Name      string
	Color     string
	SortOrder int
}

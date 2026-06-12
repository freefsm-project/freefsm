package templates

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

type DashboardData struct{}

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
}

func customerFormTitle(isNew bool) string {
	if isNew {
		return "New Customer"
	}
	return "Edit Customer"
}

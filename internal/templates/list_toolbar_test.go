package templates

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestListURLPreservesStructuredQueryState(t *testing.T) {
	query := url.Values{
		"search":      {"pump & valve"},
		"status_id":   {"7"},
		"customer_id": {"42"},
	}

	got := listURL("/jobs", query, 3)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse pagination URL: %v", err)
	}
	if parsed.Path != "/jobs" {
		t.Errorf("path = %q, want /jobs", parsed.Path)
	}
	for key, want := range map[string]string{
		"search": "pump & valve", "status_id": "7", "customer_id": "42", "page": "3",
	} {
		if got := parsed.Query().Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	if query.Get("page") != "" {
		t.Fatal("listURL mutated its input query")
	}
}

func TestAssetListContentSynchronizesToolbarAndResults(t *testing.T) {
	p := AssetListPageData{
		Page: 2, TotalPages: 3, Search: "lathe", CustomerID: 42, AssetTypeID: 3, AssetStatusID: 7,
		AssetTypes:    []SelectOption{{Value: 3, Label: "Machine"}},
		AssetStatuses: []SelectOption{{Value: 7, Label: "In service"}},
	}

	var out strings.Builder
	if err := AssetListContent(p).Render(context.Background(), &out); err != nil {
		t.Fatalf("render asset list content: %v", err)
	}
	html := out.String()
	for _, want := range []string{
		`id="asset-list-content"`,
		`class="focused-list-actions"`,
		`New Asset`,
		`Filter`,
		`aria-label="2 active filters"`,
		`name="customer_id" value="42"`,
		`name="search" value="lathe"`,
		`name="asset_type_id" value="3"`,
		`name="asset_status_id" value="7"`,
		`x-data="{ search: $el.dataset.search }"`,
		`data-search="lathe"`,
		`hx-sync="#asset-list-content:replace"`,
		`id="asset-list-content-search-filter" type="hidden" name="search" value="lathe" x-bind:value="search"`,
		`id="asset-list-content-search" type="search" name="search"`,
		`x-model="search" hx-trigger="input changed delay:300ms, search"`,
		`hx-get="/assets?customer_id=42" hx-include="#asset-list-content-search-filter"`,
		`customer_id=42&amp;search=lathe`,
		`asset_status_id=7&amp;asset_type_id=3&amp;customer_id=42&amp;page=3&amp;search=lathe`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered asset list missing %q", want)
		}
	}
	if got := strings.Count(html, `method="get"`); got != 2 {
		t.Errorf("GET form count = %d, want separate search and filter forms", got)
	}
	if got := strings.Count(html, `hx-sync="#asset-list-content:replace"`); got != 3 {
		t.Errorf("shared HTMX synchronization count = %d, want toolbar and 2 pagination links", got)
	}
	if strings.Contains(html, `hx-get="/assets?customer_id=42&amp;search=`) {
		t.Error("Clear HTMX URL duplicates the included browser search value")
	}
	if strings.Index(html, "New Asset") > strings.Index(html, "Filter") {
		t.Error("New action must precede Filter")
	}
}

func TestListContentRendersFilterControls(t *testing.T) {
	tests := []struct {
		name      string
		component templ.Component
		controls  []string
	}{
		{
			name:      "assets",
			component: AssetListContent(AssetListPageData{}),
			controls:  []string{`<select name="asset_type_id">`, `<select name="asset_status_id">`},
		},
		{
			name:      "timesheets",
			component: TimeEntriesListContent(TimeEntryListPageData{ShowUserFilter: true}),
			controls: []string{
				`<select name="user_id">`,
				`<input type="date" name="date_from"`,
				`<input type="date" name="date_to"`,
			},
		},
		{name: "customers", component: CustomerListContent(CustomerListPageData{}), controls: []string{`<select name="status">`}},
		{name: "jobs", component: JobsListContent(JobListPageData{}), controls: []string{`<select name="status_id">`}},
		{name: "projects", component: ProjectListContent(ProjectListPageData{}), controls: []string{`<select name="status_id">`}},
		{name: "estimates", component: EstimatesListContent(EstimateListPageData{}), controls: []string{`<select name="status_id">`}},
		{name: "invoices", component: InvoicesListContent(InvoiceListPageData{}), controls: []string{`<select name="status_id">`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			if err := tt.component.Render(context.Background(), &out); err != nil {
				t.Fatalf("render list content: %v", err)
			}
			html := out.String()
			panelClass := strings.Index(html, `class="focused-list-filter-panel"`)
			if panelClass < 0 {
				t.Fatal("rendered list content missing focused filter form")
			}
			formStart := strings.LastIndex(html[:panelClass], "<form")
			formEnd := strings.Index(html[panelClass:], "</form>")
			if formStart < 0 || formEnd < 0 || strings.Contains(html[formStart:panelClass], ">") {
				t.Fatal("rendered list content missing focused filter form")
			}
			panel := html[panelClass : panelClass+formEnd]
			apply := strings.Index(panel, ">Apply</button>")
			clear := strings.Index(panel, ">Clear</a>")
			if apply < 0 || clear < 0 {
				t.Fatal("focused filter form missing Apply or Clear action")
			}
			for _, control := range tt.controls {
				controlIndex := strings.Index(panel, control)
				if controlIndex < 0 || controlIndex > apply || controlIndex > clear {
					t.Errorf("filter control %q is not inside the focused filter form before Apply/Clear", control)
				}
			}
		})
	}
}

func TestPaginationSynchronizesAllHTMXLinksWithTarget(t *testing.T) {
	var out strings.Builder
	p := PaginationData{CurrentPage: 2, TotalPages: 3, BaseURL: "/assets", Target: "#asset-list-content"}
	if err := Pagination(p).Render(context.Background(), &out); err != nil {
		t.Fatalf("render pagination: %v", err)
	}

	html := out.String()
	if got := strings.Count(html, `hx-get=`); got != 2 {
		t.Fatalf("pagination HTMX link count = %d, want 2", got)
	}
	if got := strings.Count(html, `hx-sync="#asset-list-content:replace"`); got != 2 {
		t.Errorf("synchronized pagination link count = %d, want 2", got)
	}
}

func TestListToolbarOptionalControls(t *testing.T) {
	t.Run("filter mirrors an initially empty search", func(t *testing.T) {
		var out strings.Builder
		p := ListToolbarData{Action: "/assets", Target: "#asset-list-content", ShowFilter: true}
		if err := ListToolbar(p).Render(context.Background(), &out); err != nil {
			t.Fatalf("render list toolbar: %v", err)
		}
		if !strings.Contains(out.String(), `id="asset-list-content-search-filter" type="hidden" name="search" value="" x-bind:value="search"`) {
			t.Fatal("filter toolbar omitted the browser search mirror when no search has yet been applied")
		}
	})

	t.Run("items omit filter", func(t *testing.T) {
		var out strings.Builder
		if err := ItemsListContent(ItemListPageData{}).Render(context.Background(), &out); err != nil {
			t.Fatalf("render items list content: %v", err)
		}
		html := out.String()
		if strings.Contains(html, `class="focused-list-filter"`) || strings.Contains(html, `>Filter<`) {
			t.Fatal("items toolbar rendered Filter")
		}
		if !strings.Contains(html, `href="/items/new"`) {
			t.Fatal("items toolbar omitted New Item")
		}
	})

	t.Run("timesheets omit new and retain GET fallback", func(t *testing.T) {
		var out strings.Builder
		p := TimeEntryListPageData{Search: "repair", DateFrom: "2026-07-01"}
		if err := TimeEntriesListContent(p).Render(context.Background(), &out); err != nil {
			t.Fatalf("render timesheets list content: %v", err)
		}
		html := out.String()
		if strings.Contains(html, `focused-list-new`) {
			t.Fatal("timesheets toolbar rendered New")
		}
		for _, want := range []string{
			`class="focused-list-filter"`,
			`method="get" action="/time-entries"`,
			`href="/time-entries?search=repair"`,
			`hx-get="/time-entries" hx-include="#time-entry-list-content-search-filter"`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("rendered timesheets toolbar missing %q", want)
			}
		}
	})
}

func TestJobsToolbarPreservesNewPermission(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: "technician"})
	var out strings.Builder
	if err := JobsListContent(JobListPageData{}).Render(ctx, &out); err != nil {
		t.Fatalf("render jobs list content: %v", err)
	}
	if strings.Contains(out.String(), `href="/jobs/new"`) {
		t.Fatal("technician jobs toolbar rendered New Job")
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"html"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFinancialReportFilterContracts(t *testing.T) {
	for _, kind := range []authorization.FinancialReportKind{authorization.FinancialAging, authorization.FinancialCustomers, authorization.FinancialSettlement, authorization.FinancialCredit, authorization.FinancialCombined} {
		t.Run(string(kind), func(t *testing.T) {
			f, err := parseFinancialReportFilter(kind, url.Values{"customer_id": {"42"}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseFinancialReportFilter(kind, financialFilterValues(kind, f))
			if err != nil || got != f {
				t.Fatalf("filter round trip: %+v %v", got, err)
			}
			for _, query := range []string{"customer_id=0", "customer_id=-1", "customer_id=1.5", "customer_id=9223372036854775808", "customer_id=1&customer_id=2", "scope=A", "page=1", "as_of=2026-01-01", "sort=unknown", "exclude_unknown=true"} {
				q, _ := url.ParseQuery(query)
				if _, err := parseFinancialReportFilter(kind, q); err == nil {
					t.Errorf("accepted %s", query)
				}
			}
		})
	}
	for _, query := range []string{"from=2026-02-30", "from=2026-12-01&to=2026-01-01", "include_archived=1", "bucket=1_30"} {
		q, _ := url.ParseQuery(query)
		if _, err := parseFinancialReportFilter(authorization.FinancialSettlement, q); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	for _, kind := range []authorization.FinancialReportKind{authorization.FinancialAging, authorization.FinancialCustomers, authorization.FinancialCredit} {
		if _, err := parseFinancialReportFilter(kind, url.Values{"from": {"2026-01-01"}}); err == nil {
			t.Errorf("accepted activity date for %s", kind)
		}
	}
}

func TestFinancialQueryDecodeAndEarlyFailure(t *testing.T) {
	// Creating then closing this lazy pool makes Principal fail without DB I/O.
	pool, err := pgxpool.New(context.Background(), "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	auth := authorization.New(pool)
	request := func(path string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		return r.WithContext(authorization.WithActor(r.Context(), auth, authorization.Actor{UserID: 1}))
	}
	for _, raw := range []string{"view=settlements&from=%ZZ", "view=credits&customer_id=%ZZ", "view=settlements&from=2026-01-01;to=2026-02-01", "view=%ZZ"} {
		w := httptest.NewRecorder()
		financialReport(w, request(paymentsCreditsReportPath+"?"+raw))
		if w.Code != 400 {
			t.Errorf("malformed report query %q: got %d want 400", raw, w.Code)
		}
	}
	for _, spec := range financialExportSpecs() {
		for _, method := range []string{"GET", "POST"} {
			r := request(spec.Path + "?customer_id=%ZZ")
			r.Method = method
			// Simulate middleware that parsed the request and discarded its error.
			_ = r.ParseForm()
			w := httptest.NewRecorder()
			financialExportHandler(pool, spec)(w, r)
			if w.Code != 400 {
				t.Errorf("%s %s malformed pre-parsed query: got %d", method, spec.Path, w.Code)
			}
		}
	}
	for _, view := range []string{"settlements", "credits"} {
		q := url.Values{"view": {view}, "customer_id": {"42"}, "include_archived": {"true"}}
		if view == "settlements" {
			q.Set("from", "2026-01-01")
			q.Set("to", "2026-02-01")
		}
		w := httptest.NewRecorder()
		financialReport(w, request(paymentsCreditsReportPath+"?"+q.Encode()))
		if w.Code != 503 {
			t.Fatalf("dependency failure: %d", w.Code)
		}
		match := regexp.MustCompile(`href="([^"]*)">Retry</a>`).FindStringSubmatch(w.Body.String())
		if len(match) != 2 {
			t.Fatalf("missing retry: %s", w.Body.String())
		}
		u, err := url.Parse(html.UnescapeString(match[1]))
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != paymentsCreditsReportPath || u.Query().Encode() != q.Encode() {
			t.Errorf("early failure changed intent: got %s want %s", u, q.Encode())
		}
	}
}

func TestFinancialReportMoneyExactSigned(t *testing.T) {
	for value, want := range map[int64]string{0: "$0.00", -1: "-$0.01", -1234: "-$12.34", math.MinInt64: "-$92233720368547758.08", math.MaxInt64: "$92233720368547758.07"} {
		if got := financialMoney(value); got != want {
			t.Errorf("%d: %s want %s", value, got, want)
		}
	}
}

func TestFinancialReportHistoryPresentationBoundaries(t *testing.T) {
	report := services.FinancialReport{Kind: authorization.FinancialCombined, Timezone: "UTC", UnknownActivity: []services.FinancialActivityRow{{Kind: "payment_reversal", ID: "reversal", InvoiceID: 123, AmountCents: 100, NetPaymentsCents: -100}}, Credits: []services.FinancialCreditRow{{ID: "credit", PaymentID: "source", AvailableCents: 50}}, Totals: services.FinancialReportTotals{UnknownEventCount: 1, UnknownNetPaymentsCents: -100, AvailableCreditCents: 50}}
	sections := financialSections(report)
	if len(sections) != 3 || len(sections[0].Rows) != 0 || len(sections[1].Rows) != 1 {
		t.Fatalf("unknown history merged into dated activity: %+v", sections)
	}
	if !sections[1].Rows[0].Cells[3].Unknown {
		t.Fatal("unknown recording time not explicit")
	}
	for _, section := range sections {
		for _, row := range section.Rows {
			for _, cell := range row.Cells {
				if cell.URL != "" {
					t.Fatal("history/Credit acquired a live link")
				}
			}
		}
	}
	if sections[0].Measures[0].Value != "$0.00" || sections[2].Measures[0].Value != "$0.50" {
		t.Fatal("history changed independent totals")
	}
}

func TestFinancialReportFailureDoesNotRenderTotals(t *testing.T) {
	p := templates.FinancialReportPageData{Title: "Report", Path: agingReportPath, View: "invoices", Error: "Unavailable", Filters: url.Values{"customer_id": {"42"}}, Sections: []templates.FinancialSection{{Title: "Must not appear", Measures: []templates.WorkReportMeasure{{Label: "Outstanding", Value: "$0.00"}}}}}
	w := httptest.NewRecorder()
	if err := templates.FinancialReport(p).Render(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if strings.Contains(body, "Must not appear") || strings.Contains(body, "$0.00") {
		t.Fatal("failed snapshot displayed results")
	}
	if !strings.Contains(body, "customer_id=42") || !strings.Contains(body, "Retry") {
		t.Fatal("failure lost filter-preserving retry")
	}
}

func TestFinancialReportViewRequiresOfficeAuthorization(t *testing.T) {
	r := httptest.NewRequest("GET", paymentsCreditsReportPath+"?view=combined", nil)
	if _, _, err := financialView(r, url.Values{"view": {"combined"}}); err != authorization.ErrForbidden {
		t.Fatalf("missing office authorization accepted: %v", err)
	}
}

func TestFinancialExportRecoveryContracts(t *testing.T) {
	for _, spec := range financialExportSpecs() {
		t.Run(spec.Title, func(t *testing.T) {
			f, err := parseFinancialReportFilter(spec.Kind, url.Values{"customer_id": {"42"}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			selection := exports.RecoverySelection{Kind: spec.Definition.Kind(), Options: raw}
			link := financialExportSelectionURL(selection)
			u, err := url.Parse(link)
			if err != nil || u.Path != spec.Path {
				t.Fatalf("wrong recovery path: %s %v", link, err)
			}
			got, err := parseFinancialReportFilter(spec.Kind, u.Query())
			if err != nil || got != f {
				t.Fatalf("recovery lost filters: %+v %v", got, err)
			}
			if u.Query().Has("operation_id") {
				t.Fatal("recovery replayed operation")
			}
			selection.Options = json.RawMessage(`{"customer_id":42,"unknown":true}`)
			if financialExportSelectionURL(selection) != "" {
				t.Fatal("malformed recovery disclosed URL")
			}
		})
	}
}

func TestFinancialExportRejectsCachedMalformedBody(t *testing.T) {
	for _, spec := range financialExportSpecs() {
		r := httptest.NewRequest("POST", spec.Path, strings.NewReader("csrf_token=valid-token&operation_id=reviewed-operation&customer_id=%ZZ"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := r.ParseForm(); err == nil {
			t.Fatal("fixture must have a body decoding error")
		}
		if r.PostForm.Get("operation_id") == "" {
			t.Fatal("fixture must retain partial parsed fields")
		}
		r = r.WithContext(authorization.WithActor(r.Context(), authorization.New(nil), authorization.Actor{UserID: 1}))
		w := httptest.NewRecorder()
		financialExportHandler(nil, spec)(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s cached malformed body: %d", spec.Path, w.Code)
		}
	}
}

func TestFinancialDashboardAttributionNoticeRendered(t *testing.T) {
	grants := map[string]bool{}
	for _, permission := range []string{"job.view", "estimate.view", "invoice.view", "pricing.view", "customer.view", "customer.financial.view"} {
		grants[permission+"/A"] = true
	}
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{ID: 1, NavigationGrants: grants})
	for _, widget := range []string{services.WidgetTodayOverview, services.WidgetFinancialSummary} {
		for _, incomplete := range []bool{false, true} {
			p := templates.DashboardData{Stats: services.DashboardStats{NetPaymentsAttributionIncomplete: incomplete, PaymentsCollectedTodayExact: "12.34", RevenueMonthExact: "56.78"}, Widgets: []services.DashboardWidgetView{{Type: widget}}}
			w := httptest.NewRecorder()
			if err := templates.DashboardPage(p).Render(ctx, w); err != nil {
				t.Fatal(err)
			}
			body := w.Body.String()
			if !strings.Contains(body, "Net Payments") {
				t.Fatalf("%s missing Net Payments label", widget)
			}
			if got := strings.Contains(body, "historical cash events with unknown recording times are excluded"); got != incomplete {
				t.Fatalf("%s incomplete=%t: notice rendered=%t", widget, incomplete, got)
			}
			if incomplete && !strings.Contains(body, `role="status"`) {
				t.Fatal("notice missing accessible status")
			}
			denied := httptest.NewRecorder()
			if err := templates.DashboardPage(p).Render(context.Background(), denied); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(denied.Body.String(), "Net Payments") || strings.Contains(denied.Body.String(), "unknown recording times") {
				t.Fatal("unauthorized widget leaked measure or notice")
			}
		}
	}
}

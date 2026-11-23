package handlers

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestReportFiltersRetainSelectedPredicates(t *testing.T) {
	q := url.Values{"scope": {"J"}, "schedule_from": {"2026-10-01"}, "schedule_to": {"2026-11-01"}, "due_from": {"2026-10-02"}, "due_to": {"2026-10-30"}, "assignee_id": {"7,4", "7"}, "customer_id": {"9"}, "category": {"job:pending"}, "measure": {"overdue"}}
	f, err := parseWorkloadFilter(q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.AssigneeIDs, []int64{4, 7}) {
		t.Fatalf("assignees %v", f.AssigneeIDs)
	}
	canonical := workloadValues(f, authorization.Assigned)
	measures := workloadMeasures(authorization.WorkloadMeasures{Scheduled: 2}, canonical)
	for _, m := range measures {
		u, err := url.Parse(m.URL)
		if err != nil {
			t.Fatal(err)
		}
		for key, values := range canonical {
			if key == "measure" {
				continue
			}
			if !reflect.DeepEqual(u.Query()[key], values) {
				t.Errorf("%s lost %s: %s", m.Label, key, m.URL)
			}
		}
		if m.Label == "Scheduled" && u.Query().Get("measure") != "overdue,scheduled" {
			t.Fatalf("broadened existing overdue predicate: %s", m.URL)
		}
	}
	back, err := parseWorkloadFilter(canonical)
	if err != nil || !reflect.DeepEqual(f, back) {
		t.Fatalf("round trip: %#v, %v", back, err)
	}
}

func TestReportSelectedIDsNeverFallBackToUnfiltered(t *testing.T) {
	for _, value := range []string{"-1", "garbage", "999999999999999999999999"} {
		if _, err := parseJobOperationalFilter(url.Values{"customer_id": {value}}); err == nil {
			t.Errorf("Job accepted %q", value)
		}
		if _, err := parseDocumentListFilter("estimate", url.Values{"status_id": {value}}); err == nil {
			t.Errorf("Estimate accepted %q", value)
		}
		if _, err := parseEstimateCohortFilter(url.Values{"customer_id": {value}}); err == nil {
			t.Errorf("cohort accepted %q", value)
		}
	}
	if _, err := parseWorkloadFilter(url.Values{"assignee_id": {"1"}, "unassigned": {"true"}}); err == nil {
		t.Fatal("contradictory assignee selection accepted")
	}
	if _, err := parseDocumentListFilter("invoice", url.Values{"valid": {"current"}}); err == nil {
		t.Fatal("Invoice accepted Estimate-only filter")
	}
}

func TestCohortAndDocumentFilterRoundTrips(t *testing.T) {
	f := authorization.EstimateCohortFilter{CreatedFrom: "2026-01-01", CreatedTo: "2026-10-31", CustomerID: 7, Category: "estimate:accepted", Conversion: "none", Sort: "title_asc"}
	got, err := parseEstimateCohortFilter(cohortValues(f, authorization.Assigned))
	if err != nil || got != f {
		t.Fatalf("cohort round trip %#v %v", got, err)
	}
	d := authorization.DocumentListFilter{Search: "retained search", StatusID: 2, CustomerID: 7, Valid: "current", Sort: "number_asc"}
	dgot, err := parseDocumentListFilter("estimate", documentListValues(d, authorization.All))
	if err != nil || dgot != d {
		t.Fatalf("document round trip %#v %v", dgot, err)
	}
}

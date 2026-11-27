package liststate_test

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/liststate"
)

func spec() liststate.Spec {
	fields := liststate.CommonFields("relevance", "name", "name_desc")
	fields["type"] = liststate.Set("customer", "lease")
	fields["customer_id"] = liststate.Optional(liststate.Integer(1, 1<<63-1))
	fields["ending_before"] = liststate.Optional(liststate.Date)
	return liststate.Spec{Fields: fields, Defaults: url.Values{
		"q": {""}, "sort": {"relevance"}, "state": {"active"}, "page": {"1"}, "per_page": {"25"},
	}}
}

func TestParseStrictState(t *testing.T) {
	for _, raw := range []string{
		"unknown=x", "q=a&q=b", "type=lease&type=lease", "type=jobs", "state=deleted",
		"sort=random", "page=0", "page=1000001", "page=-1", "page=+1", "page=1e2",
		"page=9999999999999999999999999", "per_page=10", "customer_id=0",
		"ending_before=2026-02-30", "reset=0", "reset=1&q=", "reset=1&reset=1",
		"q=" + strings.Repeat("界", 501),
	} {
		t.Run(raw[:min(len(raw), 80)], func(t *testing.T) {
			values, err := url.ParseQuery(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := liststate.Parse(values, spec())
			if !errors.Is(err, liststate.ErrInvalid) || got.Values != nil {
				t.Fatalf("accepted malformed state: %+v %v", got, err)
			}
		})
	}
	for _, values := range []url.Values{{"q": nil}, {"q": {"\xff"}}, {"q": {strings.Repeat("a", liststate.MaxBytes)}}} {
		if _, err := liststate.Parse(values, spec()); !errors.Is(err, liststate.ErrInvalid) {
			t.Fatalf("invalid shape/encoding/size accepted: %v", err)
		}
	}
}

func TestURLPrecedenceAndCanonicalRoundTrip(t *testing.T) {
	empty, err := liststate.Parse(nil, spec())
	if err != nil || empty.Explicit || empty.Values.Get("per_page") != "25" {
		t.Fatalf("defaults: %+v %v", empty, err)
	}
	input := url.Values{"q": {""}, "type": {"lease", "customer"}, "customer_id": {""}}
	query, err := liststate.Parse(input, spec())
	if err != nil || !query.Explicit || query.Values.Get("q") != "" {
		t.Fatalf("explicit empty: %+v %v", query, err)
	}
	canonical := liststate.Canonical(query.Values)
	parsed, err := url.ParseQuery(canonical)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := liststate.Parse(parsed, spec())
	if err != nil || !reflect.DeepEqual(roundTrip.Values, query.Values) || !strings.Contains(canonical, "q=") {
		t.Fatalf("round trip: %q %+v %v", canonical, roundTrip, err)
	}
	if input["type"][0] != "lease" {
		t.Fatal("parser changed input")
	}
	query.Values["type"][0] = "changed"
	if input["type"][0] != "lease" {
		t.Fatal("parser aliased input")
	}
	reset, err := liststate.Parse(url.Values{"reset": {"1"}}, spec())
	if err != nil || !reset.Reset || reset.Explicit || len(reset.Values) != 0 {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	if liststate.Canonical(nil) != "q=" {
		t.Fatal("empty canonical URL can restore unintended saved state")
	}
}

func TestPersistentIsolationAndUnicode(t *testing.T) {
	values := url.Values{"q": {strings.Repeat("界", 500)}, "page": {"5"}, "reset": {"1"}, "scroll": {"99"}, "csrf_token": {"secret"}, "type": {"lease"}}
	persisted := liststate.Persistent(values)
	if len(persisted) != 2 || values.Get("page") != "5" {
		t.Fatalf("transport persisted or input modified: %+v", persisted)
	}
	persisted["type"][0] = "customer"
	if values.Get("type") != "lease" {
		t.Fatal("persistent state aliases caller")
	}
	if _, err := liststate.Parse(url.Values{"q": values["q"]}, spec()); err != nil {
		t.Fatalf("500 Unicode characters rejected: %v", err)
	}
	fields := map[string]func([]string) error{"filter": liststate.Text(20_000)}
	if _, err := liststate.Parse(url.Values{"filter": {strings.Repeat("界", 2000)}}, liststate.Spec{Fields: fields}); !errors.Is(err, liststate.ErrInvalid) {
		t.Fatal("encoded size limit not enforced")
	}
}

func TestFlashTransportDoesNotOverrideOrPersistQuery(t *testing.T) {
	raw := url.Values{"flash": {"Job archived"}}
	query, err := liststate.Parse(raw, spec())
	if err != nil || query.Explicit || query.Values.Has("flash") || raw.Get("flash") != "Job archived" {
		t.Fatalf("flash interpreted as query state: %+v %v", query, err)
	}
	raw.Set("q", "")
	query, err = liststate.Parse(raw, spec())
	if err != nil || !query.Explicit || query.Values.Has("flash") {
		t.Fatalf("explicit state lost beside transport: %+v %v", query, err)
	}
	if liststate.Persistent(raw).Has("flash") || strings.Contains(liststate.Canonical(raw), "flash") {
		t.Fatal("notification entered persistent/canonical state")
	}
	for _, flash := range [][]string{nil, {"one", "two"}, {"\xff"}, {"message\nsecond"}, {strings.Repeat("界", 501)}} {
		if _, err := liststate.Parse(url.Values{"flash": flash}, spec()); !errors.Is(err, liststate.ErrInvalid) {
			t.Fatalf("malformed flash accepted: %v", err)
		}
	}
	query, err = liststate.Parse(url.Values{"reset": {"1"}, "flash": {"Done"}}, spec())
	if err != nil || !query.Reset || query.Explicit {
		t.Fatalf("transport changed Reset semantics: %+v %v", query, err)
	}
	if _, err := liststate.Parse(url.Values{"notice": {"Unknown transport"}}, spec()); !errors.Is(err, liststate.ErrInvalid) {
		t.Fatal("unknown keys are no longer strict")
	}
}

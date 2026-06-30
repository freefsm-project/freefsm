package services

import (
	"testing"

	"github.com/MartialM1nd/freefsm/internal/ent"
)

func TestLocationAddressFormatsNonEmptyParts(t *testing.T) {
	t.Parallel()

	got := LocationAddress(&ent.Location{
		Address1: "123 Main St",
		Address2: "Suite 4",
		City:     "Denver",
		State:    "CO",
		ZipCode:  "80202",
	})
	if want := "123 Main St, Suite 4, Denver, CO, 80202"; got != want {
		t.Fatalf("LocationAddress() = %q, want %q", got, want)
	}
}

func TestGeocodeSearchURLBuildsNominatimRequest(t *testing.T) {
	t.Parallel()

	reqURL, err := geocodeSearchURL("https://nominatim.openstreetmap.org", "123 Main St, Denver, CO")
	if err != nil {
		t.Fatalf("geocodeSearchURL returned error: %v", err)
	}
	if got, want := reqURL.Scheme, "https"; got != want {
		t.Fatalf("scheme = %q, want %q", got, want)
	}
	if got, want := reqURL.Host, "nominatim.openstreetmap.org"; got != want {
		t.Fatalf("host = %q, want %q", got, want)
	}
	if got, want := reqURL.Path, "/search"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if got, want := reqURL.Query().Get("q"), "123 Main St, Denver, CO"; got != want {
		t.Fatalf("q = %q, want %q", got, want)
	}
	if got, want := reqURL.Query().Get("format"), "jsonv2"; got != want {
		t.Fatalf("format = %q, want %q", got, want)
	}
	if got, want := reqURL.Query().Get("limit"), "1"; got != want {
		t.Fatalf("limit = %q, want %q", got, want)
	}
}

func TestGeocodeSearchURLDoesNotDuplicateSearchPath(t *testing.T) {
	t.Parallel()

	reqURL, err := geocodeSearchURL("https://nominatim.openstreetmap.org/search", "123 Main St")
	if err != nil {
		t.Fatalf("geocodeSearchURL returned error: %v", err)
	}
	if got, want := reqURL.Path, "/search"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

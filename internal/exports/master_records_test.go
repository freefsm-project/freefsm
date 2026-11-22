package exports

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestMasterRecordColumnContracts(t *testing.T) {
	cases := []struct {
		d                         Definition
		kind, permission, columns string
	}{
		{Contact{}, "contact-operational-v1", "contact.export", "contact_id,first_name,last_name,role_title,email,phone,service_notes,notes,primary_customer_default,customer_id,customer_label,customer_visibility,job_links_json,job_links_visibility,archived"},
		{Location{}, "location-operational-v1", "location.export", "location_id,title,address_1,address_2,city,state,zip_code,latitude,longitude,access_notes,service_notes,notes,primary_customer_default,customer_id,customer_label,customer_visibility,job_links_json,job_links_visibility,archived"},
		{Project{}, "project-operational-v1", "project.export", "project_id,name,description,status_id,status_label,status_category,displayed_progress_percent,retained_manual_progress_percent,planned_start_date,planned_end_date,service_notes,notes,legacy_review_reasons_json,legacy_captured_timezone,custom_fields_json,customer_id,customer_label,customer_visibility,location_id,location_label,location_visibility,archived"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if tc.d.Kind() != tc.kind || tc.d.Permission() != tc.permission || tc.d.Scope() != authorization.All {
				t.Fatal("versioned adapter identity changed")
			}
			if got := strings.Join(tc.d.Columns(), ","); got != tc.columns {
				t.Fatalf("fixed CSV contract changed: %s", got)
			}
		})
	}
}

func TestMasterRecordOptionsAndUnprivilegedDenial(t *testing.T) {
	for _, d := range []Definition{Contact{}, Location{}, Project{}} {
		t.Run(d.Kind(), func(t *testing.T) {
			for _, raw := range []string{`null`, `{}`, `[]`, `{"sort":"id"}`, `{"sort":"name","page":1}`, `{"sort":"name","customer_id":-1}`, `{"sort":"name","status_id":-1}`, `{"sort":"name","archived":"true"}`, `{"sort":"name"} {}`, `{"sort":"name"} junk`} {
				if err := d.Validate(json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
					t.Errorf("accepted invalid options %s: %v", raw, err)
				}
			}
			for _, sort := range []string{"name", "name_desc", "updated"} {
				raw := json.RawMessage(`{"sort":"` + sort + `","search":"%'_","archived":true}`)
				if err := d.Validate(raw); err != nil {
					t.Fatal(err)
				}
				if err := d.Authorize(authorization.Principal{}, raw); !errors.Is(err, authorization.ErrForbidden) {
					t.Fatalf("unprivileged authorization: %v", err)
				}
				if err := d.Snapshot(context.Background(), nil, authorization.Principal{}, raw, func(Record) error { t.Fatal("denied export emitted a row"); return nil }); !errors.Is(err, authorization.ErrForbidden) {
					t.Fatalf("snapshot must deny before querying: %v", err)
				}
			}
			if err := d.Recheck(context.Background(), nil, authorization.Principal{}, json.RawMessage(`{"id":1}`)); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("recheck must deny before querying: %v", err)
			}
			for _, raw := range []string{`null`, `{}`, `{"id":-1}`, `invalid`} {
				if err := d.Recheck(context.Background(), nil, authorization.Principal{}, json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
					t.Fatalf("invalid reference %s: %v", raw, err)
				}
			}
		})
	}
}

func TestMasterRecordApplicableSelectionFilters(t *testing.T) {
	customer := json.RawMessage(`{"sort":"name","customer_id":42}`)
	status := json.RawMessage(`{"sort":"name","status_id":42}`)
	for _, d := range []Definition{Contact{}, Location{}} {
		if err := d.Validate(customer); err != nil {
			t.Fatal(err)
		}
		if err := d.Validate(status); !errors.Is(err, ErrConflict) {
			t.Fatalf("non-Project Status filter accepted: %v", err)
		}
	}
	if err := (Project{}).Validate(status); err != nil {
		t.Fatal(err)
	}
	if err := (Project{}).Validate(customer); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsupported Project Customer filter accepted: %v", err)
	}
}

func TestMasterRecordRetainedCellBoundFailsWithoutTruncation(t *testing.T) {
	value := strings.Repeat("x", masterCellLimit)
	if err := validateMasterCells([]string{"identity", value}); err != nil {
		t.Fatal(err)
	}
	if err := validateMasterCells([]string{"identity", value + "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("oversized retained value must fail generation: %v", err)
	}
}

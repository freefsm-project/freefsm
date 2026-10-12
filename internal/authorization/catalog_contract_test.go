package authorization_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Expected outcomes come from PER-01–14 and S6's export/parent/facet rules,
// independently of the catalog rows that SaveRole validates.
func TestCatalogViewPrerequisiteContract(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'catalog@test','Catalog','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	g := func(p string, s authorization.Scope) authorization.Grant {
		return authorization.Grant{Permission: p, Scope: s}
	}
	for i, tc := range []struct {
		name    string
		grants  []authorization.Grant
		allowed bool
	}{
		{"Estimate assigned export", []authorization.Grant{g("estimate.view", authorization.Assigned), g("estimate.export", authorization.Assigned)}, true},
		{"Invoice assigned export", []authorization.Grant{g("invoice.view", authorization.Assigned), g("invoice.export", authorization.Assigned)}, true},
		{"Job export without View", []authorization.Grant{g("job.export", authorization.Assigned)}, false},
		{"Job export compatible View", []authorization.Grant{g("job.view", authorization.Assigned), g("job.export", authorization.Assigned)}, true},
		{"Job export narrower View", []authorization.Grant{g("job.view", authorization.Assigned), g("job.export", authorization.All)}, false},
		{"GPS export without GPS View", []authorization.Grant{g("time.view", authorization.Own), g("time.gps.export", authorization.Own)}, false},
		{"GPS export compatible facet", []authorization.Grant{g("time.view", authorization.Own), g("time.gps.view", authorization.Own), g("time.gps.export", authorization.Own)}, true},
		{"GPS export narrower facet", []authorization.Grant{g("time.view", authorization.All), g("time.gps.view", authorization.Own), g("time.gps.export", authorization.Assigned)}, false},
		{"Customer-only Invoice pricing needs no Job View", []authorization.Grant{g("invoice.view", authorization.All), g("pricing.view", authorization.All)}, true},
		{"Assigned Estimate pricing", []authorization.Grant{g("estimate.view", authorization.Assigned), g("pricing.view", authorization.Assigned)}, true},
		{"Pricing without parent", []authorization.Grant{g("pricing.view", authorization.All)}, false},
		{"Activity export without parent", []authorization.Grant{g("activity.export", authorization.Assigned)}, false},
		{"Activity export for visible Invoice", []authorization.Grant{g("invoice.view", authorization.Assigned), g("activity.export", authorization.Assigned)}, true},
		{"Activity export cannot widen Invoice View", []authorization.Grant{g("invoice.view", authorization.Assigned), g("activity.export", authorization.All)}, false},
		{"File download without parent", []authorization.Grant{g("file.download", authorization.Assigned)}, false},
		{"File download for visible Job", []authorization.Grant{g("job.view", authorization.Assigned), g("file.download", authorization.Assigned)}, true},
		{"Assigned files cannot borrow Customer master View", []authorization.Grant{g("customer.view", authorization.All), g("file.download", authorization.Assigned)}, false},
		{"Own file removal needs parent View", []authorization.Grant{g("file.remove", authorization.Own)}, false},
		{"Own file removal with Job View", []authorization.Grant{g("job.view", authorization.Assigned), g("file.remove", authorization.Own)}, true},
		{"Own file removal cannot borrow Time View", []authorization.Grant{g("time.view", authorization.Own), g("file.remove", authorization.Own)}, false},
		{"Restore needs archived View", []authorization.Grant{g("customer.view", authorization.All), g("customer.restore", authorization.All)}, false},
		{"Restore with archived View", []authorization.Grant{g("customer.view", authorization.All), g("customer.archived.view", authorization.All), g("customer.restore", authorization.All)}, true},
		{"Refund needs Credit View", []authorization.Grant{g("customer.view", authorization.All), g("customer.financial.view", authorization.All), g("credit.refund", authorization.All)}, false},
		{"Refund with Credit View", []authorization.Grant{g("customer.view", authorization.All), g("customer.financial.view", authorization.All), g("credit.view", authorization.All), g("credit.refund", authorization.All)}, true},
		{"Reversal needs settlement history View", []authorization.Grant{g("customer.view", authorization.All), g("customer.financial.view", authorization.All), g("payment.reverse", authorization.All)}, false},
		{"Reversal with settlement history View", []authorization.Grant{g("customer.view", authorization.All), g("customer.financial.view", authorization.All), g("settlement.view", authorization.All), g("payment.reverse", authorization.All)}, true},
		{"Credit application needs selected Invoice View", []authorization.Grant{g("customer.view", authorization.All), g("customer.financial.view", authorization.All), g("credit.view", authorization.All), g("credit.apply", authorization.All)}, false},
		{"Credit application with both selected views", []authorization.Grant{g("customer.view", authorization.All), g("customer.financial.view", authorization.All), g("credit.view", authorization.All), g("invoice.view", authorization.All), g("credit.apply", authorization.All)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, fmt.Sprintf("case-%d", i), tc.grants)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}

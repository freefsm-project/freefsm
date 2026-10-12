package authorization_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Machine-key translation of PRD PER-01–14 (including S8/S11/S13/S16
// amendments). This is an independent contract, not generated from SQL rows.
func canonicalScopes() map[string]string {
	m := map[string]string{}
	add := func(scopes string, keys ...string) {
		for _, key := range keys {
			m[key] = scopes
		}
	}
	group := func(scopes string, resources, actions string) {
		for _, r := range strings.Fields(resources) {
			for _, a := range strings.Fields(actions) {
				add(scopes, r+"."+a)
			}
		}
	}
	group("AC", "customer contact location asset project", "view")
	group("A", "customer contact location asset project", "create edit")
	add("A", "customer.financial.view", "directory.view", "project.status", "asset.transfer", "asset.place", "asset.return", "asset.history.correct")
	group("AJ", "job", "view status subtask.complete subtask.reopen field_notes.edit fields.edit")
	group("A", "job", "create edit relations.edit schedule assign subtask.manage")
	group("A", "item pricebook", "view create edit export")
	group("A", "cost", "view edit export")
	add("AJ", "pricing.view")
	group("AJ", "estimate invoice", "view create edit status provenance.view pdf.generate pdf.download delivery.request export")
	add("AJ", "estimate.convert", "invoice.finalize", "invoice.revert", "payment.record")
	add("A", "payment.credit", "credit.apply", "credit.refund", "payment.reverse", "credit.application.reverse", "credit.refund.reverse", "credit.view", "settlement.view", "credit.export", "settlement.export")
	group("AJO", "time", "view create correct void export gps.view gps.export")
	group("O", "time", "clock_in clock_out gps.capture")
	add("AJ", "comment.add", "file.download", "file.upload", "tag.attach", "tag.detach", "job.export", "activity.export")
	add("AJO", "comment.remove", "file.rename", "file.remove")
	group("AJ", "service_entry", "view create revise")
	add("A", "service_entry.export", "lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "lease.export")
	group("A", "customer contact location asset project job estimate invoice item lease", "archive archived.view restore")
	group("A", "customer contact location asset project", "export")
	add("A", "customer.financial.export")
	group("U", "configuration.company configuration.documents configuration.mail configuration.maps configuration.workflow configuration.assets configuration.tags configuration.fields", "view manage")
	add("U", "configuration.mail.test")
	group("S", "security.password security.session security.offline security.open_tracking", "view manage")
	add("S", "user.access.view", "user.create", "user.invite", "user.identity.edit", "user.role.assign", "user.enabled.edit", "user.welcome.resend", "user.recovery.resend", "user.password.require_change", "user.sessions.revoke",
		"role.create", "role.edit", "role.rename", "role.remove", "audit.view", "audit.export", "dashboard.defaults", "backup.create", "backup.restore", "email.enable")
	// Existing nonassignable protected-authority marker, not an additional
	// user action and never a replacement for a specific permission check.
	add("S", "administration")
	return m
}

func canonicalBaseView(key string, scope authorization.Scope) string {
	for _, resource := range strings.Fields("customer contact location asset project") {
		if key == resource+".view" {
			if scope == authorization.Context {
				return "job.view"
			}
			return ""
		}
	}
	if strings.HasSuffix(key, ".restore") && scope == authorization.All {
		return strings.TrimSuffix(key, ".restore") + ".archived.view"
	}
	if strings.HasSuffix(key, ".archived.view") {
		return strings.TrimSuffix(key, ".archived.view") + ".view"
	}
	switch key {
	case "pricing.view", "activity.export", "comment.add", "comment.remove", "file.download", "file.upload", "file.rename", "file.remove", "tag.attach", "tag.detach":
		return "" // fixed parent alternatives
	case "customer.financial.view", "credit.view", "settlement.view":
		if key == "customer.financial.view" {
			return "customer.view"
		}
		return "customer.financial.view"
	case "customer.financial.export":
		return "customer.financial.view"
	case "pricebook.view", "cost.view":
		return "item.view"
	case "payment.record":
		return "invoice.view"
	case "payment.credit":
		return "customer.financial.view"
	case "credit.apply", "credit.refund", "credit.export":
		return "credit.view"
	case "payment.reverse", "credit.application.reverse", "credit.refund.reverse", "settlement.export":
		return "settlement.view"
	case "lease.billing.view":
		return "lease.view"
	case "lease.billing.manage":
		return "lease.billing.view"
	case "time.gps.view":
		return "time.view"
	case "time.gps.export":
		return "time.gps.view"
	case "audit.export":
		return "audit.view"
	case "configuration.mail.test":
		return "configuration.mail.view"
	}
	if strings.HasPrefix(key, "configuration.") || strings.HasPrefix(key, "security.") {
		if strings.HasSuffix(key, ".manage") {
			return strings.TrimSuffix(key, ".manage") + ".view"
		}
		return ""
	}
	for _, resource := range strings.Fields("customer contact location asset project job estimate invoice item pricebook cost time service_entry lease") {
		if strings.HasPrefix(key, resource+".") && key != resource+".view" {
			return resource + ".view"
		}
	}
	return "" // sealed full-authority actions and directory View
}

func TestEveryCatalogActionLocksItsCanonicalBaseView(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'dependencies@test','Dependencies','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	for key, scopes := range canonicalScopes() {
		for _, letter := range scopes {
			scope := authorization.Scope(string(letter))
			base := canonicalBaseView(key, scope)
			var actual *string
			if err := pool.QueryRow(ctx, `SELECT prerequisite FROM authorization_permissions WHERE permission=$1 AND scope=$2`, key, scope).Scan(&actual); err != nil {
				t.Fatal(err)
			}
			if (base == "") != (actual == nil) || actual != nil && *actual != base {
				t.Errorf("%s/%s prerequisite=%v want %q", key, scope, actual, base)
			}
			parentBound := key == "pricing.view" || key == "activity.export" || strings.HasPrefix(key, "comment.") || strings.HasPrefix(key, "file.") || key == "tag.attach" || key == "tag.detach"
			if scope != authorization.Sealed && (base != "" || parentBound) {
				if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Viewless "+key+string(scope), []authorization.Grant{{Permission: key, Scope: scope}}); err == nil {
					t.Errorf("%s/%s accepted with no required View", key, scope)
				}
			}
		}
	}
}

func TestEntireCanonicalCatalogScopesAndAssignment(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	actual := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT permission,scope FROM authorization_permissions ORDER BY permission,scope`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, scope string
		if err := rows.Scan(&key, &scope); err != nil {
			t.Fatal(err)
		}
		actual[key] += scope
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalScopes()
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("closed scope catalog mismatch\nactual=%v\nexpected=%v", actual, want)
	}
	var admin int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'matrix@test','Matrix','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	// Supply all compatible assignable views so an invalid action/scope cannot
	// hide behind a missing prerequisite. These are explicit view choices, not
	// permissions learned from the implementation under test.
	views := []authorization.Grant{}
	for _, key := range strings.Fields("customer.view customer.financial.view contact.view location.view asset.view project.view job.view estimate.view invoice.view item.view pricebook.view cost.view credit.view settlement.view time.view time.gps.view service_entry.view lease.view lease.billing.view customer.archived.view contact.archived.view location.archived.view asset.archived.view project.archived.view job.archived.view estimate.archived.view invoice.archived.view item.archived.view lease.archived.view") {
		views = append(views, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	for _, area := range strings.Fields("company documents mail maps workflow assets tags fields") {
		views = append(views, authorization.Grant{Permission: "configuration." + area + ".view", Scope: authorization.Configuration})
	}
	keys := []string{}
	for key := range want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, scope := range []authorization.Scope{authorization.All, authorization.Assigned, authorization.Context, authorization.Own, authorization.Configuration, authorization.Sealed} {
			t.Run(key+"/"+string(scope), func(t *testing.T) {
				grants := append([]authorization.Grant{}, views...)
				candidate := authorization.Grant{Permission: key, Scope: scope}
				duplicate := false
				for _, view := range grants {
					if view == candidate {
						duplicate = true
					}
				}
				if !duplicate {
					grants = append(grants, candidate)
				}
				_, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, fmt.Sprintf("matrix %s %s", key, scope), grants)
				allowed := scope != authorization.Sealed && strings.Contains(want[key], string(scope))
				if (err == nil) != allowed {
					t.Fatalf("scope=%s allowed=%v err=%v", scope, allowed, err)
				}
			})
		}
	}
}

func TestCanonicalExportAndSealedDefaults(t *testing.T) {
	pool, _ := fixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		role              int64
		permission, scope string
		want              bool
	}{
		{1, "estimate.export", "A", true}, {1, "invoice.export", "A", true}, {2, "estimate.export", "A", true}, {2, "invoice.export", "A", true},
		{3, "estimate.export", "J", false}, {3, "invoice.export", "J", false}, {3, "job.export", "J", false}, {3, "time.gps.export", "O", false},
		{3, "invoice.pdf.download", "J", true}, {3, "time.clock_in", "O", true}, {2, "configuration.company.manage", "U", false}, {2, "audit.export", "S", false},
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_grants WHERE role_id=$1 AND permission=$2 AND scope=$3)`, tc.role, tc.permission, tc.scope).Scan(&exists); err != nil || exists != tc.want {
			t.Errorf("default %d %s/%s = %v want %v: %v", tc.role, tc.permission, tc.scope, exists, tc.want, err)
		}
	}
	var technicianExports int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_grants WHERE role_id=3 AND permission LIKE '%.export'`).Scan(&technicianExports); err != nil || technicianExports != 0 {
		t.Fatalf("Technician unexpectedly received %d exports: %v", technicianExports, err)
	}
}

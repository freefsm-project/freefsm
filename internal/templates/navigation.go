package templates

import (
	"context"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

// Navigation uses effective grants loaded with this request, never Role labels.
// Scopes are deliberately specified per destination: Context master access does
// not authorize collection discovery, and configuration never implies records.
func navigationPermission(ctx context.Context, permission string, scopes ...string) bool {
	u, ok := middleware.UserFromContext(ctx)
	if !ok || u == nil {
		return false
	}
	for _, scope := range scopes {
		if u.NavigationGrants[permission+"/"+scope] {
			return true
		}
	}
	return false
}

type navigationLink struct{ URL, Label string }

func navigationRecordPermission(ctx context.Context, typ string, id int64, action string) bool {
	req, ok := authorization.FromContext(ctx)
	return ok && req.Service.ParentAccess(ctx, req.Actor, typ, id, typ+"."+action) == nil
}

func operationalLinks(ctx context.Context) []navigationLink {
	var links []navigationLink
	if navigationPermission(ctx, "job.view", "A", "J") {
		links = append(links, navigationLink{"/schedule", "Schedule"})
	}
	for _, domain := range []struct {
		name, label string
		scopes      []string
	}{
		{"customer", "Customers", []string{"A"}}, {"contact", "Contacts", []string{"A"}},
		{"location", "Locations", []string{"A"}}, {"asset", "Assets", []string{"A"}},
		{"job", "Jobs", []string{"A", "J"}}, {"project", "Projects", []string{"A"}},
		{"estimate", "Estimates", []string{"A", "J"}}, {"invoice", "Invoices", []string{"A", "J"}},
		{"item", "Items", []string{"A"}},
	} {
		if navigationPermission(ctx, domain.name+".view", domain.scopes...) {
			links = append(links, navigationLink{"/" + domain.name + "s", domain.label})
		}
	}
	if navigationPermission(ctx, "time.view", "A", "J", "O") {
		links = append(links, navigationLink{"/time-entries", "Timesheets"})
	}
	if len(links) > 0 {
		links = append(links, navigationLink{"/activity", "Activity"})
	}
	return links
}

func creationLinks(ctx context.Context) []navigationLink {
	var links []navigationLink
	for _, domain := range []navigationLink{{"customer", "Customer"}, {"job", "Job"}, {"project", "Project"}, {"asset", "Asset"}, {"estimate", "Estimate"}, {"invoice", "Invoice"}, {"item", "Item"}} {
		if navigationPermission(ctx, domain.URL+".create", "A") {
			links = append(links, navigationLink{"/" + domain.URL + "s/new", "New " + domain.Label})
		}
	}
	return links
}

func navigationSearch(ctx context.Context) bool {
	return len(operationalLinks(ctx)) > 0
}

func navigationDocuments(ctx context.Context) bool {
	return navigationPermission(ctx, "job.view", "A") && navigationPermission(ctx, "estimate.view", "A") && navigationPermission(ctx, "invoice.view", "A") && navigationPermission(ctx, "pricing.view", "A")
}

func navigationOverview(ctx context.Context) bool {
	return navigationDocuments(ctx) && navigationPermission(ctx, "customer.view", "A") && navigationPermission(ctx, "customer.financial.view", "A")
}

func navigationReturnURL(ctx context.Context, path string) string {
	switch path {
	case "/assets", "/projects", "/estimates", "/invoices", "/jobs":
		for _, link := range operationalLinks(ctx) {
			if link.URL == path {
				return path
			}
		}
		return "/"
	default:
		return path
	}
}

func configurationLinks(ctx context.Context) []navigationLink {
	var links []navigationLink
	add := func(allowed bool, path, label string) {
		if allowed {
			links = append(links, navigationLink{path, label})
		}
	}
	add(navigationPermission(ctx, "configuration.company.view", "U") || navigationPermission(ctx, "configuration.documents.view", "U"), "/settings", "Company and document settings")
	add(navigationPermission(ctx, "configuration.mail.view", "U"), "/settings/mail", "Mail settings")
	add(navigationPermission(ctx, "configuration.maps.view", "U"), "/settings/maps", "Map settings")
	for _, workflow := range []navigationLink{{"job", "Job workflow"}, {"project", "Project workflow"}, {"estimate", "Estimate workflow"}, {"invoice", "Invoice workflow"}} {
		add(navigationPermission(ctx, "configuration.workflow.view", "U"), "/settings/statuses/"+workflow.URL, workflow.Label)
	}
	add(navigationPermission(ctx, "configuration.assets.view", "U"), "/settings/assets", "Asset vocabulary")
	add(navigationPermission(ctx, "configuration.tags.view", "U"), "/tags", "Tags")
	add(navigationPermission(ctx, "configuration.fields.view", "U"), "/settings/custom-fields", "Custom Fields")
	add(canReviewAuthorization(ctx), "/settings/authorization", "Authorization review")
	add(navigationPermission(ctx, "security.password.view", "S"), "/settings/advanced", "Security settings")
	add(navigationPermission(ctx, "security.open_tracking.view", "S"), "/settings/open-tracking", "Open tracking policy")
	add(navigationPermission(ctx, "backup.create", "S") || navigationPermission(ctx, "backup.restore", "S"), "/settings/backup", "Backup")
	add(navigationPermission(ctx, "audit.view", "S"), "/settings/audit", "Protected audit")
	add(canAdmin(ctx), "/users", "Users")
	return links
}

func navigationCurrent(ctx context.Context, path string) string {
	// /settings is a destination itself, not a parent of independently granted
	// configuration destinations. Only one configuration section is current.
	if path == "/settings" {
		if middleware.PathFromContext(ctx) == path {
			return "page"
		}
		return "false"
	}
	if isActivePath(ctx, path) {
		links := append(operationalLinks(ctx), configurationLinks(ctx)...)
		links = append(links, creationLinks(ctx)...)
		for _, link := range links {
			if len(link.URL) > len(path) && strings.HasPrefix(link.URL, path+"/") && isActivePath(ctx, link.URL) {
				return "false"
			}
		}
		return "page"
	}
	return "false"
}

func creationNavigationActive(ctx context.Context) bool {
	for _, link := range creationLinks(ctx) {
		if isActivePath(ctx, link.URL) {
			return true
		}
	}
	return false
}

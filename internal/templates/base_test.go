package templates

import (
	"context"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestConfigurationNavigationIncludesIndependentWorkflowDestinations(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{
		Name:                   "Admin",
		Role:                   "admin",
		ProtectedAdministrator: true,
		NavigationGrants:       map[string]bool{"configuration.company.view/U": true, "configuration.workflow.view/U": true},
	})

	var out strings.Builder
	if err := Layout("Test").Render(ctx, &out); err != nil {
		t.Fatal(err)
	}

	html := out.String()
	if !strings.Contains(html, `aria-label="Configuration"`) || !strings.Contains(html, `href="/settings"`) {
		t.Fatal("administrator navigation was not rendered")
	}
	for _, typ := range []string{"job", "project", "estimate", "invoice"} {
		if strings.Count(html, `href="/settings/statuses/`+typ+`"`) != 1 {
			t.Fatalf("workflow %s must have one discoverable destination", typ)
		}
	}
}

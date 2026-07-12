package templates

import (
	"context"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestAdministrationMenuOmitsStatusSettingsShortcut(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{
		Name: "Admin",
		Role: "admin",
	})

	var out strings.Builder
	if err := Layout("Test").Render(ctx, &out); err != nil {
		t.Fatal(err)
	}

	html := out.String()
	if !strings.Contains(html, "Administration") || !strings.Contains(html, `href="/settings"`) {
		t.Fatal("administrator navigation was not rendered")
	}
	if strings.Contains(html, `href="/settings/statuses/job"`) || strings.Contains(html, ">Status Settings<") {
		t.Fatal("administrator navigation contains duplicate status settings shortcut")
	}
}

package templates

import (
	"context"
	"strings"
	"testing"
)

func renderUserShow(t *testing.T, page UserDetailPage) string {
	t.Helper()

	var out strings.Builder
	if err := UserShow(page).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestUserShowRendersResendWelcomeWhenEligible(t *testing.T) {
	html := renderUserShow(t, UserDetailPage{
		User:             UserRow{ID: 42, Name: "Invited User", IsActive: false},
		CanResendWelcome: true,
	})

	if !strings.Contains(html, `action="/users/42/resend-welcome"`) || !strings.Contains(html, ">Resend Welcome</button>") {
		t.Fatal("eligible user page did not render the resend welcome action")
	}
	assertUserShowOtherActions(t, html, "Enable")
}

func TestUserShowOmitsResendWelcomeWhenIneligible(t *testing.T) {
	tests := []struct {
		name         string
		user         UserRow
		toggleAction string
	}{
		{name: "active", user: UserRow{ID: 42, Name: "Active User", IsActive: true}, toggleAction: "Disable"},
		{name: "disabled", user: UserRow{ID: 42, Name: "Disabled User", IsActive: false}, toggleAction: "Enable"},
		{name: "established", user: UserRow{ID: 42, Name: "Established User", IsActive: true}, toggleAction: "Disable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderUserShow(t, UserDetailPage{User: tt.user, CanResendWelcome: false})

			if strings.Contains(html, "/users/42/resend-welcome") || strings.Contains(html, ">Resend Welcome</button>") {
				t.Fatal("ineligible user page rendered the resend welcome action")
			}
			assertUserShowOtherActions(t, html, tt.toggleAction)
		})
	}
}

func assertUserShowOtherActions(t *testing.T, html, toggleAction string) {
	t.Helper()

	for _, want := range []string{
		`href="/users/42/edit"`,
		`action="/users/42/disable"`,
		">" + toggleAction + "</button>",
		`action="/users/42/reset-password"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("user page missing action markup %q", want)
		}
	}
}

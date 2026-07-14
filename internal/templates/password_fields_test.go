package templates

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPasswordFieldModuleDefinesAccessibleIndependentControls(t *testing.T) {
	source := readTemplateContractFile(t, "password_fields.templ")

	for _, want := range []string{
		`templ PasswordField(options PasswordFieldOptions)`,
		`templ NewPasswordFields(options NewPasswordFieldsOptions)`,
		`label for={ options.ID }`,
		`label for={ options.ConfirmID }`,
		`aria-controls={ options.ID }`,
		`aria-controls={ options.ConfirmID }`,
		`name="confirm_password"`,
		`autocomplete={ options.Autocomplete }`,
		`data-password-toggle-label>Show</span>`,
		`aria-pressed="false"`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("password field module is missing %q", want)
		}
	}
	if got := strings.Count(source, `data-password-toggle aria-controls=`); got != 3 {
		t.Errorf("each single/pair field definition must have an adjacent toggle; found %d", got)
	}
	if strings.Contains(source, "data-password-value") || strings.Contains(source, "data-value") {
		t.Error("password field markup must not expose password values in data attributes")
	}
}

func TestPasswordFlowsUseSharedSingleAndPairComponents(t *testing.T) {
	tests := []struct {
		file        string
		singleCount int
		pairCount   int
		wants       []string
	}{
		{"setup.templ", 1, 1, []string{`ID: "setup-token"`, `Autocomplete: "off"`, `Autocomplete: "new-password"`}},
		{"login.templ", 1, 0, []string{`ID: "login-password"`, `Autocomplete: "current-password"`}},
		{"reset_password.templ", 0, 1, []string{`Name: "password"`, `Autocomplete: "new-password"`}},
		{"accept_invite.templ", 0, 1, []string{`Name: "password"`, `Autocomplete: "new-password"`}},
		{"users_form.templ", 0, 1, []string{`WrapperID: "password_fields"`, `Autocomplete: "new-password"`}},
		{"users_show.templ", 0, 1, []string{`Name: "password"`, `Autocomplete: "new-password"`}},
		{"change_password.templ", 1, 1, []string{`Name: "current_password"`, `Autocomplete: "current-password"`, `ID: "new_password"`, `Name: "new_password"`, `ConfirmID: "confirm_password"`, `Autocomplete: "new-password"`, `document.getElementById('new_password')`}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			source := readTemplateContractFile(t, tt.file)
			if got := strings.Count(source, `@PasswordField(PasswordFieldOptions{`); got != tt.singleCount {
				t.Errorf("%s has %d single password fields, want %d", tt.file, got, tt.singleCount)
			}
			if got := strings.Count(source, `@NewPasswordFields(NewPasswordFieldsOptions{`); got != tt.pairCount {
				t.Errorf("%s has %d new-password pairs, want %d", tt.file, got, tt.pairCount)
			}
			for _, want := range tt.wants {
				if !strings.Contains(source, want) {
					t.Errorf("%s is missing password contract %q", tt.file, want)
				}
			}
		})
	}
}

func TestPasswordFlowInputIDsAreUnique(t *testing.T) {
	idPattern := regexp.MustCompile(`(?:ID|ConfirmID): "([^"]+)"`)
	seen := make(map[string]string)
	for _, file := range []string{
		"setup.templ", "login.templ", "reset_password.templ", "accept_invite.templ",
		"users_form.templ", "users_show.templ", "change_password.templ",
	} {
		for _, match := range idPattern.FindAllStringSubmatch(readTemplateContractFile(t, file), -1) {
			if previous, exists := seen[match[1]]; exists {
				t.Errorf("password input ID %q is reused by %s and %s", match[1], previous, file)
			}
			seen[match[1]] = file
		}
	}
}

func TestDirectUserPasswordPairCannotSubmitStaleValues(t *testing.T) {
	source := readTemplateContractFile(t, "users_form.templ")
	for _, want := range []string{
		`Disabled: true`,
		`var inputs = fields.querySelectorAll('input');`,
		`fields.classList.toggle('is-hidden', !show);`,
		`input.disabled = !show;`,
		`input.required = show;`,
		`if (!show) input.value = '';`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("direct-user password synchronization is missing %q", want)
		}
	}

	html := renderComponent(t, UserForm(UserFormData{User: &UserDetail{}, IsNew: true}))
	if got := strings.Count(html, " disabled"); got != 2 {
		t.Errorf("direct-user form rendered %d initially disabled fields, want 2", got)
	}
	for _, name := range []string{`name="password"`, `name="confirm_password"`} {
		if !strings.Contains(html, name) {
			t.Errorf("direct-user form is missing %s", name)
		}
	}
}

func TestPasswordRevealScriptIsGlobalVersionedAndDelegated(t *testing.T) {
	base := readTemplateContractFile(t, "base.templ")
	if !strings.Contains(base, `<script src={ templ.URL(staticAsset("/static/js/password-fields.js")) } defer></script>`) {
		t.Error("base layout must load the versioned password field script with defer")
	}

	script := readTemplateContractFile(t, "../../cmd/freefsm/static/js/password-fields.js")
	for _, want := range []string{
		`window.__freefsmPasswordFieldsInstalled`,
		`document.addEventListener('click'`,
		`event.target.closest('[data-password-toggle]')`,
		`document.getElementById(button.getAttribute('aria-controls'))`,
		`input.type = revealing ? 'text' : 'password';`,
		`button.setAttribute('aria-pressed'`,
		`button.setAttribute('aria-label'`,
		`label.textContent = revealing ? 'Hide' : 'Show';`,
		`input.focus({ preventScroll: true });`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("password reveal script is missing %q", want)
		}
	}
}

func TestSMTPPasswordRemainsUnenhanced(t *testing.T) {
	settings := readTemplateContractFile(t, "settings.templ")
	if !strings.Contains(settings, `type="password" name="smtp_password"`) {
		t.Fatal("SMTP password input is missing")
	}
	if strings.Contains(settings, `data-password-toggle`) || strings.Contains(settings, `@PasswordField(`) || strings.Contains(settings, `@NewPasswordFields(`) {
		t.Error("SMTP settings must not use password reveal controls")
	}
}

func readTemplateContractFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

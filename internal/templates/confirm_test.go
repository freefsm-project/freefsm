package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplatesUseHTMXConfirmForInteractiveConfirmations(t *testing.T) {
	patterns := []string{
		`onsubmit="return confirm(`,
		`onclick="return confirm(`,
	}

	matches, err := filepath.Glob("*.templ")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}

	for _, path := range matches {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		for _, pattern := range patterns {
			if strings.Contains(text, pattern) {
				t.Fatalf("%s contains %q; use hx-confirm so htmx boosted requests respect cancellations", path, pattern)
			}
		}
	}
}

func TestJobQuickCreateDialogsAreOutsideJobForm(t *testing.T) {
	content, err := os.ReadFile("jobs_form.templ")
	if err != nil {
		t.Fatalf("read jobs_form.templ: %v", err)
	}
	text := string(content)

	formStart := strings.Index(text, `<form action={ templ.URL(jobFormAction(p.Job.ID)) }`)
	if formStart == -1 {
		t.Fatal("job form start not found")
	}
	formEnd := strings.Index(text[formStart:], `</form>`)
	if formEnd == -1 {
		t.Fatal("job form end not found")
	}
	formBody := text[formStart : formStart+formEnd]

	for _, id := range []string{
		`id="job-project-dialog"`,
		`id="job-location-dialog"`,
		`id="job-asset-dialog"`,
		`id="job-contact-dialog"`,
		`id="job-project-name" required`,
		`id="job-location-title" required`,
		`id="job-asset-name" required`,
		`id="job-asset-type-id" required`,
		`id="job-contact-first-name" required`,
	} {
		if strings.Contains(formBody, id) {
			t.Fatalf("job form contains modal-only field %s; this can block Save via browser validation", id)
		}
	}
}

func TestJobModalResetIsScopedToDialog(t *testing.T) {
	content, err := os.ReadFile("jobs_form.templ")
	if err != nil {
		t.Fatalf("read jobs_form.templ: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, `const dialog = document.getElementById('job-' + prefix + '-dialog');`) {
		t.Fatal("resetJobModalFields should find the modal dialog before clearing fields")
	}
	if !strings.Contains(text, `dialog.querySelectorAll('[id^="job-' + prefix + '-"]')`) {
		t.Fatal("resetJobModalFields should clear only fields inside the modal dialog")
	}
	if strings.Contains(text, `document.querySelectorAll('[id^="job-' + prefix + '-"]')`) {
		t.Fatal("resetJobModalFields must not query the whole document; it resets main job selects")
	}
}

func TestJobOptionRefreshIgnoresStaleResponses(t *testing.T) {
	content, err := os.ReadFile("jobs_form.templ")
	if err != nil {
		t.Fatalf("read jobs_form.templ: %v", err)
	}
	text := string(content)

	for _, snippet := range []string{
		`const requestID = String((Number(select.dataset.refreshRequest || '0') || 0) + 1);`,
		`select.dataset.refreshRequest = requestID;`,
		`if (select.dataset.refreshRequest !== requestID) return;`,
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("refreshOptions missing stale-response guard %q", snippet)
		}
	}
}

func TestJobInlineCreateSelectsCreatedOptionDirectly(t *testing.T) {
	content, err := os.ReadFile("jobs_form.templ")
	if err != nil {
		t.Fatalf("read jobs_form.templ: %v", err)
	}
	text := string(content)

	for _, snippet := range []string{
		`function selectInlineOption(select, created)`,
		`if (![...select.options].some(option => option.value === value))`,
		`select.appendChild(option);`,
		`select.dataset.selected = value;`,
		`select.value = value;`,
		`selectInlineOption(jobLocationSelect, created);`,
		`selectInlineOption(contactSelect, created);`,
		`selectInlineOption(jobAssetSelect, created);`,
		`selectInlineOption(jobProjectSelect, created);`,
	} {
		if !strings.Contains(text, snippet) {
			t.Fatalf("job inline create flow missing direct created-option selection %q", snippet)
		}
	}
}

func TestDocumentCreateItemModalsDoNotBlockFormSubmit(t *testing.T) {
	for _, path := range []string{"invoices_form.templ", "estimates_form.templ"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)

		if !strings.Contains(text, `@LineItemEditor(LineItemEditorData{`) {
			t.Fatalf("%s should invoke the shared line-item editor", path)
		}
	}

	content, err := os.ReadFile("line_item_editor.templ")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, `x-model="newItem.name" placeholder="Item name" required`) {
		t.Fatal("create item modal name field is required inside the main form; this can block Save")
	}
	if !strings.Contains(text, `this.createItemError = 'Item name is required.'`) {
		t.Fatal("shared editor should keep JS validation for create item modal name")
	}
}

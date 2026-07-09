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

	formStart := strings.Index(text, `<form action={ templ.URL(jobFormAction(p.IsNew, p.Job.ID)) }`)
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

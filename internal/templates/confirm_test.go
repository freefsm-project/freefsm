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

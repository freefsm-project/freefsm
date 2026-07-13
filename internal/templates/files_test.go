package templates

import (
	"os"
	"strings"
	"testing"
)

func TestFileUploadWidgetRendersProgressiveMultiFileContract(t *testing.T) {
	html := renderComponent(t, FileUploadWidget("customer", 42, "/customers/42"))

	for _, want := range []string{
		`class="files-drop-form"`,
		`action="/files?object_type=customer&amp;object_id=42"`,
		`hx-post="/files?object_type=customer&amp;object_id=42"`,
		`type="file" name="file" required`,
		`class="files-upload-status" aria-live="polite" aria-atomic="false" hidden`,
		`class="files-upload-error" hidden`,
		`class="files-upload-list"`,
		`href="/customers/42" class="files-upload-refresh" hidden`,
		`Refresh attachments`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered file widget does not contain %q", want)
		}
	}
	if strings.Contains(html, `name="file" multiple`) || strings.Contains(html, `multiple name="file"`) {
		t.Error("server-rendered file input must remain single-file without JavaScript")
	}
}

func TestFileUploadWidgetSourceSafelyBuildsAuthorizationQuery(t *testing.T) {
	content, err := os.ReadFile("files.templ")
	if err != nil {
		t.Fatalf("read files.templ: %v", err)
	}
	source := string(content)

	if !strings.Contains(source, `fmt.Sprintf("/files?object_type=%s&object_id=%d", url.QueryEscape(objectType), objectID)`) {
		t.Error("upload URL must use a fixed local path, escaped object type, and decimal object ID")
	}
	if got := strings.Count(source, `templ.URL(fileUploadURL(objectType, objectID))`); got != 2 {
		t.Errorf("action and hx-post must both use the Templ-safe upload URL; found %d uses", got)
	}
}

func TestFileWidgetScriptDefinesBoundedIdempotentUploadOrchestration(t *testing.T) {
	content, err := os.ReadFile("files.templ")
	if err != nil {
		t.Fatalf("read files.templ: %v", err)
	}
	source := string(content)

	for _, want := range []string{
		"function filesUploadLimit()",
		"return 10;",
		"if (files.length <= filesUploadLimit()) return true;",
		"input.multiple = true;",
		"if (form.dataset.filesEnhanced === 'true') return;",
		"form.addEventListener('submit', filesHandleSubmit);",
		"filesQueueUploads(form, files);",
		"const body = new FormData(form);",
		"body.set('file', item.file);",
		"headers: { 'HX-Request': 'true' }",
		"Math.min(2, files.length)",
		"await Promise.all(workers);",
		"window.location.assign(filesSafeRedirect(queue.redirect));",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("file upload orchestration is missing %q", want)
		}
	}

	dropStart := strings.Index(source, "function filesHandleDrop")
	modalStart := strings.Index(source, "function filesOpenModal")
	if dropStart == -1 || modalStart == -1 || dropStart >= modalStart {
		t.Fatal("could not isolate shared upload orchestration")
	}
	uploadSource := source[dropStart:modalStart]
	if got := strings.Count(uploadSource, "filesQueueUploads(form, files);"); got != 2 {
		t.Errorf("picker and drop should each use the shared queue; found %d calls", got)
	}
	if strings.Contains(uploadSource, "alert(") {
		t.Error("upload failures must render inline instead of using alerts")
	}
}

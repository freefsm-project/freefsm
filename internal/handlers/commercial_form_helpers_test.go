package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

// Positive legacy scenarios now submit the real form's concurrency controls.
// Never repair explicit or partially supplied metadata: omission/stale probes
// continue to use raw requests and must be rejected by production routing.
func currentCommercialFormValues(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values) url.Values {
	t.Helper()
	match := regexp.MustCompile(`^/(estimates|invoices)/([1-9][0-9]*)(/(custom-fields|status|finalize))?$`).FindStringSubmatch(path)
	if len(match) == 0 || cookie == nil || values.Has("save_action_id") || values.Has("save_revision") {
		return values
	}
	base := "/" + match[1] + "/" + match[2]
	formPath := base + "/edit"
	if match[4] == "status" || match[4] == "finalize" {
		formPath = base
	}
	r := httptest.NewRequest("GET", formPath, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		return values
	}
	body := w.Body.String()
	if form := regexp.MustCompile(`(?s)<form[^>]*action="` + regexp.QuoteMeta(path) + `"[^>]*>(.*?)</form>`).FindStringSubmatch(body); len(form) == 2 {
		body = form[1]
	}
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(body)
	if len(revision) != 2 {
		t.Fatalf("%s omitted the commercial revision", formPath)
	}
	out := url.Values{}
	for key, v := range values {
		out[key] = append([]string(nil), v...)
	}
	out.Set("save_action_id", newOperationKey())
	out.Set("save_revision", revision[1])
	return out
}

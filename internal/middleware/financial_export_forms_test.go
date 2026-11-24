package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/justinas/nosurf"
)

func TestFinancialExportBodyBeforeCSRF(t *testing.T) {
	for _, path := range []string{"/reports/invoice-aging/export", "/reports/customer-financials/export", "/reports/settlements/export", "/reports/credits/export", "/reports/payments-credits/export"} {
		t.Run(path, func(t *testing.T) {
			called := false
			h := BrowserCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.Write([]byte(nosurf.Token(r)))
					return
				}
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			get := httptest.NewRecorder()
			h.ServeHTTP(get, httptest.NewRequest("GET", "http://example.com"+path, nil))
			token := get.Body.String()
			for _, scenario := range []struct {
				suffix             string
				cached, validToken bool
				status             int
			}{
				{"&customer_id=%ZZ", false, true, 400},
				{"&customer_id=%ZZ", true, true, 400},
				{"&customer_id=42", false, true, 204},
				{"&customer_id=42", false, false, 400},
			} {
				called = false
				csrf := token
				if !scenario.validToken {
					csrf = "invalid"
				}
				body := "csrf_token=" + url.QueryEscape(csrf) + "&operation_id=reviewed-operation" + scenario.suffix
				r := httptest.NewRequest("POST", "http://example.com"+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Referer", "http://example.com"+path)
				for _, cookie := range get.Result().Cookies() {
					r.AddCookie(cookie)
				}
				if scenario.cached {
					_ = r.ParseForm()
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != scenario.status {
					t.Errorf("cached=%t token=%t suffix=%s: status=%d want %d", scenario.cached, scenario.validToken, scenario.suffix, w.Code, scenario.status)
				}
				if called != (scenario.status == 204) {
					t.Errorf("unsafe downstream reach: called=%t status=%d", called, w.Code)
				}
			}
		})
	}
}

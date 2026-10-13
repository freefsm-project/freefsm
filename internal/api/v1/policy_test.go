package v1

import (
	"net/http/httptest"
	"testing"
)

func TestPairingIPLimitHTTP(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))
	for i := 0; i < 21; i++ {
		r := httptest.NewRequest("POST", "/pairings", nil)
		r.RemoteAddr = "192.0.2.76:1234"
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if i == 20 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
			t.Fatalf("IP budget: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestLocalProtocolReferenceHTTP(t *testing.T) {
	api := New(testDependencies(&fakeUsers{}, &fakeJobs{}))
	for _, path := range []string{"/openapi.json", "/docs", "/reference", "/reference.js"} {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("local reference %s: %d", path, w.Code)
		}
	}
}

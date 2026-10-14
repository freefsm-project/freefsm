package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransferRangeCORS(t *testing.T) {
	h := ResponsePolicy([]string{"https://client.test"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("OPTIONS", "/files/1", nil)
	r.Header.Set("Origin", "https://client.test")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Headers", "Authorization, Range, If-Range")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("authorized resumable preflight=%d", w.Code)
	}
	for _, header := range []string{"ETag", "Repr-Digest", "Content-Range"} {
		if !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), header) {
			t.Fatalf("missing exposed integrity header %s", header)
		}
	}
}

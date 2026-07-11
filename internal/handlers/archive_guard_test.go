package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MartialM1nd/freefsm/internal/objectref"
	"github.com/go-chi/chi/v5"
)

func TestRequireActiveObjectUsesDirectory(t *testing.T) {
	activeRef := objectref.New(objectref.TypeCustomer, 1)
	inactiveRef := objectref.New(objectref.TypeCustomer, 2)
	errorRef := objectref.New(objectref.TypeCustomer, 3)
	dir := &objectref.FakeDirectory{
		Active: map[objectref.Ref]bool{activeRef: true, inactiveRef: false, errorRef: true},
		Any:    map[objectref.Ref]bool{activeRef: true, inactiveRef: true, errorRef: true},
		Errors: map[objectref.Ref]error{errorRef: errors.New("database down")},
	}

	r := chi.NewRouter()
	r.With(requireActiveObject(dir, objectref.TypeCustomer)).Get("/customers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	cases := []struct {
		name string
		path string
		want int
	}{
		{name: "active", path: "/customers/1", want: http.StatusNoContent},
		{name: "inactive", path: "/customers/2", want: http.StatusForbidden},
		{name: "adapter error", path: "/customers/3", want: http.StatusInternalServerError},
		{name: "bad id", path: "/customers/nope", want: http.StatusNotFound},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
		})
	}
}

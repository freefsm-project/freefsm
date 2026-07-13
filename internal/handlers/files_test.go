package handlers

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestOneUploadedFileRejectsMultipleFileParts(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, name := range []string{"first.txt", "second.txt"} {
		part, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/files", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	if err := r.ParseMultipartForm(1024); err != nil {
		t.Fatal(err)
	}
	defer r.MultipartForm.RemoveAll()

	if _, err := oneUploadedFile(r.MultipartForm); err == nil {
		t.Fatal("oneUploadedFile accepted multiple file parts")
	}
}

func TestWriteUploadSuccessRedirectsByRequestType(t *testing.T) {
	t.Run("HTMX", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/files", nil)
		r.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		writeUploadSuccess(w, r, "/customers/1")
		if w.Code != http.StatusOK || w.Header().Get("HX-Redirect") != "/customers/1" || w.Header().Get("Location") != "" {
			t.Fatalf("status=%d HX-Redirect=%q Location=%q", w.Code, w.Header().Get("HX-Redirect"), w.Header().Get("Location"))
		}
	})

	t.Run("ordinary form", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/files", nil)
		w := httptest.NewRecorder()
		writeUploadSuccess(w, r, "/customers/1")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/customers/1" || w.Header().Get("HX-Redirect") != "" {
			t.Fatalf("status=%d Location=%q HX-Redirect=%q", w.Code, w.Header().Get("Location"), w.Header().Get("HX-Redirect"))
		}
	})
}

func TestSafeLocalRedirect(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "local path", input: "/customers/42", want: "/customers/42"},
		{name: "local path and query", input: "/customers/42?tab=files&view=all", want: "/customers/42?tab=files&view=all"},
		{name: "empty", input: "", want: "/"},
		{name: "relative", input: "customers/42", want: "/"},
		{name: "absolute URL", input: "https://evil.example/path", want: "/"},
		{name: "scheme relative", input: "//evil.example/path", want: "/"},
		{name: "triple slash", input: "///evil.example/path", want: "/"},
		{name: "backslash", input: `/\evil.example`, want: "/"},
		{name: "encoded backslash", input: `/%5cevil.example`, want: "/"},
		{name: "double encoded backslash", input: `/%255cevil.example`, want: "/"},
		{name: "encoded scheme relative", input: `/%2f%2fevil.example`, want: "/"},
		{name: "scheme without slashes", input: `javascript:alert(1)`, want: "/"},
		{name: "raw control", input: "/safe\r\nLocation: https://evil.example", want: "/"},
		{name: "encoded control", input: `/safe%0d%0aLocation`, want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeLocalRedirect(tt.input); got != tt.want {
				t.Fatalf("safeLocalRedirect(%q)=%q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUploadRejectsBeforeReadingMultipartBody(t *testing.T) {
	const companyID int64 = 101
	ref := objectref.New(objectref.TypeCustomer, 42)
	activeDirectory := &objectref.FakeDirectory{
		Active:           map[objectref.Ref]bool{ref: true},
		Any:              map[objectref.Ref]bool{ref: true},
		TargetCompanyIDs: map[objectref.Ref]*int64{ref: int64TestPointer(companyID)},
	}
	missingDirectory := &objectref.FakeDirectory{
		Active:           map[objectref.Ref]bool{},
		Any:              map[objectref.Ref]bool{},
		TargetCompanyIDs: map[objectref.Ref]*int64{},
	}

	tests := []struct {
		name      string
		targetURL string
		role      string
		directory objectref.Directory
		wantCode  int
	}{
		{name: "invalid query", targetURL: "/files", role: "admin", directory: activeDirectory, wantCode: http.StatusBadRequest},
		{name: "missing tenant target", targetURL: "/files?object_type=customer&object_id=42", role: "admin", directory: missingDirectory, wantCode: http.StatusNotFound},
		{name: "policy denied", targetURL: "/files?object_type=customer&object_id=42", role: "unknown", directory: activeDirectory, wantCode: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileSvc := services.NewFileService(nil, tt.directory, t.TempDir(), 1024)
			h := NewFileHandler(fileSvc, nil, services.NewPolicyService(nil, tt.directory), tt.directory)
			r := httptest.NewRequest(http.MethodPost, tt.targetURL, io.NopCloser(panicReader{}))
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserKey, &middleware.UserInfo{ID: 1, CompanyID: companyID, Role: tt.role}))
			w := httptest.NewRecorder()

			h.Upload(w, r)
			if w.Code != tt.wantCode {
				t.Fatalf("status=%d, want %d", w.Code, tt.wantCode)
			}
		})
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("multipart body read before authorization")
}

func int64TestPointer(value int64) *int64 {
	return &value
}

func TestUploadTargetFromQuery(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		wantError bool
	}{
		{name: "valid", targetURL: "/files?object_type=customer&object_id=42"},
		{name: "missing type", targetURL: "/files?object_id=42", wantError: true},
		{name: "missing ID", targetURL: "/files?object_type=customer", wantError: true},
		{name: "invalid ID", targetURL: "/files?object_type=customer&object_id=nope", wantError: true},
		{name: "zero ID", targetURL: "/files?object_type=customer&object_id=0", wantError: true},
		{name: "unknown type", targetURL: "/files?object_type=unknown&object_id=42", wantError: true},
		{name: "duplicate type", targetURL: "/files?object_type=customer&object_type=job&object_id=42", wantError: true},
		{name: "duplicate ID", targetURL: "/files?object_type=customer&object_id=42&object_id=43", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tt.targetURL, nil)
			ref, err := uploadTargetFromQuery(r)
			if (err != nil) != tt.wantError {
				t.Fatalf("uploadTargetFromQuery() ref=%v err=%v, wantError=%v", ref, err, tt.wantError)
			}
		})
	}
}

package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContactUpdateParamsFromRequestCanClearOptionalFields(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("first_name=Jane&last_name=&email=&phone=&notes="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}

	params := contactUpdateParamsFromRequest(req)

	assertStringPtr(t, params.FirstName, "Jane", "FirstName")
	assertStringPtr(t, params.LastName, "", "LastName")
	assertStringPtr(t, params.Email, "", "Email")
	assertStringPtr(t, params.Phone, "", "Phone")
	assertStringPtr(t, params.Notes, "", "Notes")
}

func assertStringPtr(t *testing.T, got *string, want, field string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want pointer to %q", field, want)
	}
	if *got != want {
		t.Fatalf("%s = %q, want %q", field, *got, want)
	}
}

package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/freefsm-project/freefsm/internal/config"
)

func TestAbsoluteAppURLUsesConfiguredPublicURL(t *testing.T) {
	req := httptest.NewRequest("GET", "http://internal.local/forgot-password", nil)
	cfg := &config.Config{PublicURL: "https://fsm.example.com/"}

	got := absoluteAppURL(cfg, req, "/reset-password?token=abc")
	want := "https://fsm.example.com/reset-password?token=abc"
	if got != want {
		t.Fatalf("absoluteAppURL() = %q, want %q", got, want)
	}
}

func TestAbsoluteAppURLFallsBackToRequestHost(t *testing.T) {
	req := httptest.NewRequest("GET", "http://internal.local/forgot-password", nil)

	got := absoluteAppURL(&config.Config{}, req, "/login")
	want := "http://internal.local/login"
	if got != want {
		t.Fatalf("absoluteAppURL() = %q, want %q", got, want)
	}
}

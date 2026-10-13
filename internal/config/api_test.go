package config

import (
	"strings"
	"testing"
)

func TestAPIConfigurationRejectsInsecureOriginsAndInvalidBudgets(t *testing.T) {
	t.Setenv("FREEFSM_SESSION_SECRET", "configuration-test")
	t.Setenv("FREEFSM_SETUP_TOKEN", "configuration-test")
	for _, origin := range []string{"*", "https://*.example.test", "http://example.test", "https://example.test/", "https://example.test/path", "https://user:password@example.test", "https://example.test?x=y", "https://example.test#fragment"} {
		t.Run(origin, func(t *testing.T) {
			t.Setenv("FREEFSM_API_CORS_ORIGINS", origin)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FREEFSM_API_CORS_ORIGINS") {
				t.Fatal("insecure CORS configuration accepted")
			}
		})
	}
	for _, value := range []string{"0", "-1", "not-a-number", ""} {
		t.Run("limit-"+value, func(t *testing.T) {
			t.Setenv("FREEFSM_API_PER_USER", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FREEFSM_API_PER_USER") {
				t.Fatal("invalid limit accepted")
			}
		})
	}
	t.Setenv("FREEFSM_API_CORS_ORIGINS", "https://field.example.test,https://other.example.test:8443")
	t.Setenv("FREEFSM_API_PER_USER", "42")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.API
	if c.RequestsPerUser != 42 || c.AuthPerLogin != 5 || c.AuthPerIP != 20 || c.SyncPerClient != 12 || len(c.CORSOrigins) != 2 {
		t.Fatalf("configured/default budgets=%+v", c)
	}
}

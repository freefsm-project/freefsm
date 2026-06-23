package config

import "testing"

func TestLoadRequiresSetupToken(t *testing.T) {
	t.Setenv("FREEFSM_SESSION_SECRET", "secret")
	t.Setenv("FREEFSM_SETUP_TOKEN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want setup token error")
	}
}

func TestLoadValidatesPublicURL(t *testing.T) {
	t.Setenv("FREEFSM_SESSION_SECRET", "secret")
	t.Setenv("FREEFSM_SETUP_TOKEN", "setup")
	t.Setenv("FREEFSM_PUBLIC_URL", "not-a-url")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want public URL error")
	}
}

func TestLoadTrimsPublicURLTrailingSlash(t *testing.T) {
	t.Setenv("FREEFSM_SESSION_SECRET", "secret")
	t.Setenv("FREEFSM_SETUP_TOKEN", "setup")
	t.Setenv("FREEFSM_PUBLIC_URL", "https://fsm.example.com/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PublicURL != "https://fsm.example.com" {
		t.Fatalf("PublicURL = %q, want %q", cfg.PublicURL, "https://fsm.example.com")
	}
}

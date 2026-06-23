package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerSetsProductionTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	srv := newHTTPServer(":3000", handler)

	if srv.Addr != ":3000" {
		t.Fatalf("Addr = %q, want %q", srv.Addr, ":3000")
	}
	if srv.Handler != handler {
		t.Fatal("Handler was not set")
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 5s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %s, want 15s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 60*time.Second {
		t.Fatalf("WriteTimeout = %s, want 60s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s, want 60s", srv.IdleTimeout)
	}
}

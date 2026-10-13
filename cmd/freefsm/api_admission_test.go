package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestAPIAdmissionFailuresUseTypedProtocolErrors(t *testing.T) {
	for _, maintenance := range []bool{true, false} {
		t.Run(map[bool]string{true: "maintenance", false: "connection refresh"}[maintenance], func(t *testing.T) {
			control, err := instancecontrol.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if maintenance {
				if err := control.KeepClosed(); err != nil {
					t.Fatal(err)
				}
			}
			downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unavailable API reached database routes") })
			app := newApplicationHandler(downstream, downstream, instanceRoutes{control: control, refresh: func() error { return errors.New("private dependency failure") }, backup: http.NotFoundHandler()})
			w := httptest.NewRecorder()
			app.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/pairings", nil))
			if w.Code != 503 {
				t.Fatalf("admission status=%d", w.Code)
			}
			var body struct {
				Error struct {
					Code      string `json:"code"`
					RequestID string `json:"request_id"`
					Recovery  string `json:"recovery_disposition"`
					Retry     int    `json:"retry_after_seconds"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("untyped admission error: %v", err)
			}
			if body.Error.Code != "unavailable" || body.Error.RequestID == "" || body.Error.RequestID != w.Header().Get("X-Request-ID") || body.Error.Recovery != "retry" || body.Error.Retry != 5 || w.Header().Get("Retry-After") != "5" {
				t.Fatalf("incomplete typed admission error: %s", w.Body.String())
			}
		})
	}
}

func TestAPIAdmissionConfiguredCORS(t *testing.T) {
	for _, maintenance := range []bool{true, false} {
		control, err := instancecontrol.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if maintenance {
			if err := control.KeepClosed(); err != nil {
				t.Fatal(err)
			}
		}
		api := apiv1.New(apiv1.Dependencies{Config: config.APIConfig{CORSOrigins: []string{"https://client.example"}}})
		app := newApplicationHandler(api, http.NotFoundHandler(), instanceRoutes{control: control, refresh: func() error { return errors.New("private dependency") }, backup: http.NotFoundHandler(), apiOrigins: []string{"https://client.example"}})
		for _, origin := range []string{"https://client.example", "https://untrusted.example"} {
			r := httptest.NewRequest("POST", "/api/v1/pairings", nil)
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if origin == "https://client.example" {
				if w.Code != 503 || w.Header().Get("Access-Control-Allow-Origin") != origin || !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "Retry-After") {
					t.Fatalf("allowed client cannot read admission error: %d %v", w.Code, w.Header())
				}
			} else if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("untrusted origin granted")
			}
			if w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("credentialed CORS")
			}
		}
	}
}

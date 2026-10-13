package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

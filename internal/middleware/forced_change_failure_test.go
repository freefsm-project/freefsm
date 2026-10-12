package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type unavailablePasswordState struct{}

func (unavailablePasswordState) MustChangePassword(context.Context, int64) (bool, error) {
	return false, errors.New("storage unavailable")
}

func TestForcePasswordChangeFailsClosedOnSelectiveStorageFailure(t *testing.T) {
	called := false
	h := ForcePasswordChange(unavailablePasswordState{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest("GET", "/jobs", nil)
	r = r.WithContext(context.WithValue(r.Context(), UserKey, &UserInfo{ID: 1}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if called || w.Code != 503 {
		t.Fatalf("failed state check reached normal handler=%v status=%d", called, w.Code)
	}
}

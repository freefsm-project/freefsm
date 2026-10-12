package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
)

type saveHistoryContextKey struct{}

// Production config.Load requires SessionSecret. Unconfigured embedded test
// routers get an unpredictable per-router key, never a fixed/public default.
// Changing a key fails closed for intent comparison and absent-history proof;
// recorded actor-owned receipt lookup does not depend on the key.
func saveProtectionKey(secret string) []byte {
	if secret != "" {
		return []byte(secret)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("cannot initialize online save protection")
	}
	return key
}

func saveHistoryTag(key []byte, actor authorization.Actor) string {
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "freefsm/online-save/history/v1\x00%d\x00%s", actor.UserID, actor.SessionHash)
	return hex.EncodeToString(h.Sum(nil))
}

func submittedSaveHistory(r *http.Request) string {
	if tag := r.Header.Get("X-FreeFSM-History"); tag != "" {
		return tag
	}
	return r.PostForm.Get("save_history")
}

func sameSaveHistory(r *http.Request, supplied string) bool {
	expected, _ := r.Context().Value(saveHistoryContextKey{}).(string)
	return supplied != "" && expected != "" && hmac.Equal([]byte(supplied), []byte(expected))
}

type saveHistoryReceipt struct {
	saveaction.Receipt
	HistoryChanged bool `json:"history_changed,omitempty"`
}

func classifySaveHistory(r *http.Request, receipt saveaction.Receipt) saveHistoryReceipt {
	result := saveHistoryReceipt{Receipt: receipt}
	if receipt.Disposition == "not_received" && !sameSaveHistory(r, submittedSaveHistory(r)) {
		result.Disposition = "review"
		result.HistoryChanged = true
		result.RetrySafe = false
		// Missing history provides no trustworthy original target or effect facts.
		// The current-authority home allows review without guessing a POST URL.
		result.ReviewPath = "/"
	}
	return result
}

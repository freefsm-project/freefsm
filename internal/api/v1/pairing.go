package v1

import (
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/http"
)

func (h *handler) startPairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceName  string `json:"device_name"`
		OperationID string `json:"operation_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if h.deps.Authorization == nil {
		writeError(w, 503, "unavailable", "pairing unavailable")
		return
	}
	out, err := h.deps.Authorization.StartPairing(r.Context(), in.OperationID, in.DeviceName)
	if err != nil {
		pairingError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (h *handler) redeemPairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode  string `json:"device_code"`
		OperationID string `json:"operation_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if h.deps.Authorization == nil {
		writeError(w, 503, "unavailable", "pairing unavailable")
		return
	}
	token, err := h.deps.Authorization.RedeemPairing(r.Context(), in.DeviceCode, in.OperationID)
	if err != nil {
		pairingError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"token": token, "token_type": "Bearer", "inactivity_seconds": 2592000})
}
func (h *handler) cancelPairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode  string `json:"device_code"`
		OperationID string `json:"operation_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if h.deps.Authorization == nil {
		writeError(w, 503, "unavailable", "pairing unavailable")
		return
	}
	if err := h.deps.Authorization.CancelPairing(r.Context(), in.DeviceCode, in.OperationID); err != nil {
		pairingError(w, err)
		return
	}
	w.WriteHeader(204)
}
func pairingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrPairingInvalid):
		writeError(w, 400, "invalid_request", "valid operation ID, device label and protocol secrets are required")
	case errors.Is(err, authorization.ErrPairingUnavailable), errors.Is(err, authorization.ErrForbidden), errors.Is(err, authorization.ErrPending):
		writeError(w, 409, "pairing_unavailable", "pairing is pending, expired, cancelled, consumed or no longer authorized; review before starting again")
	default:
		writeError(w, 503, "unavailable", "pairing unavailable; no success acknowledged")
	}
}

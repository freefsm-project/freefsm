package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type settlementService interface {
	RecordBoundedPayment(context.Context, settlement.Actor, settlement.RecordPaymentRequest) (settlement.Result, error)
	RecordPayment(context.Context, settlement.Actor, settlement.RecordPaymentRequest) (settlement.Result, error)
	ApplyCredit(context.Context, settlement.Actor, settlement.ApplyCreditRequest) (settlement.Result, error)
	RefundCredit(context.Context, settlement.Actor, settlement.RefundCreditRequest) (settlement.Result, error)
	ReversePayment(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error)
	ReverseCreditApplication(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error)
	ReverseRefund(context.Context, settlement.Actor, settlement.ReverseRequest) (settlement.Result, error)
	InvoiceSettlement(context.Context, settlement.Actor, int64) (settlement.InvoiceSettlement, error)
	GrantedInvoiceSettlement(context.Context, settlement.Actor, int64) (settlement.InvoiceSettlement, error)
	CustomerSettlement(context.Context, settlement.Actor, int64) (settlement.CustomerSettlement, error)
	CustomerMasterSettlement(context.Context, settlement.Actor, int64) (settlement.CustomerSettlement, error)
}

func settlementActor(ctx context.Context) (settlement.Actor, bool) {
	u, ok := middleware.UserFromContext(ctx)
	if !ok || u == nil {
		return settlement.Actor{}, false
	}
	return settlement.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, true
}

func parseCents(value string) (int64, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ".")
	if value == "" || len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && (parts[1] == "" || len(parts[1]) > 2)) {
		return 0, errors.New("amount must be a positive decimal with at most two decimal places")
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, errors.New("amount must be a positive decimal with at most two decimal places")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > (int64(^uint64(0)>>1)-99)/100 {
		return 0, errors.New("amount is too large")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fraction, _ = strconv.ParseInt(parts[1], 10, 64)
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	cents := whole*100 + fraction
	if cents <= 0 {
		return 0, errors.New("amount must be greater than zero")
	}
	return cents, nil
}

func parseUUID(value, label string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s", label)
	}
	return id, nil
}

func writeSettlementError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusUnprocessableEntity, "settlement request could not be processed"
	switch {
	case errors.Is(err, settlement.ErrForbidden), errors.Is(err, authorization.ErrForbidden):
		status, message = http.StatusForbidden, "settlement operation forbidden"
	case errors.Is(err, settlement.ErrNotFound), errors.Is(err, pgx.ErrNoRows), errors.Is(err, authorization.ErrNotFound):
		status, message = http.StatusNotFound, "settlement record not found"
	case errors.Is(err, settlement.ErrIdempotencyConflict), errors.Is(err, authorization.ErrConflict):
		status, message = http.StatusConflict, "idempotency key was already used for another request"
	case errors.Is(err, settlement.ErrDependency):
		status, message = http.StatusConflict, "operation has active settlement dependencies"
	case errors.Is(err, settlement.ErrArchived), errors.Is(err, settlement.ErrCustomerArchived), errors.Is(err, settlement.ErrAlreadyReversed), errors.Is(err, settlement.ErrVoid), errors.Is(err, settlement.ErrNotFinalized):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, settlement.ErrInvalidAmount):
		message = "invalid settlement amount"
	case errors.Is(err, settlement.ErrPaymentReversalReason):
		message = err.Error()
	case errors.Is(err, settlement.ErrInsufficientCredit):
		message = err.Error()
	default:
		validation := err.Error()
		if strings.Contains(validation, "date") || strings.Contains(validation, "reason is required") || strings.Contains(validation, "idempotency key is required") {
			http.Error(w, validation, http.StatusUnprocessableEntity)
			return
		}
		internalServerError(w, r, "settlement operation", err)
		return
	}
	http.Error(w, message, status)
}

func newOperationKey() string { return uuid.NewString() }

func paymentOperationReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	receipt, err := settlement.New(req.Service.Pool).PaymentReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": receipt})
}

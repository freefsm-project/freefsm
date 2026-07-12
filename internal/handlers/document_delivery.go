package handlers

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func safeEmailHTML(body string) string {
	return "<!doctype html><html><body><p>" + strings.ReplaceAll(html.EscapeString(body), "\n", "<br>\n") + "</p></body></html>"
}

func writeDeliveryError(w http.ResponseWriter, r *http.Request, err error, data templates.DocumentEmailData) {
	switch {
	case errors.Is(err, delivery.ErrForbidden):
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, delivery.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, delivery.ErrIdempotencyConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, delivery.ErrInvalid):
		data.Error = err.Error()
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = templates.DocumentEmailCompose(data).Render(r.Context(), w)
	default:
		internalServerError(w, r, "queue document delivery", err)
	}
}

func deliveryHistoryRows(items []delivery.Summary) []templates.DeliveryHistoryRow {
	rows := make([]templates.DeliveryHistoryRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, templates.DeliveryHistoryRow{ID: item.ID, State: item.State, Recipients: strings.Join(item.To, ", "), QueuedAt: deliveryTime(item.CreatedAt), AcceptedAt: deliveryTime(item.AcceptedAt), DeliveredAt: deliveryTime(item.DeliveredAt), BouncedAt: deliveryTime(item.BouncedAt), FailedAt: deliveryTime(item.FailedAt), OpenedAt: deliveryTime(item.LastOpenAt), OpenCount: item.OpenCount, Attempts: item.LifetimeAttemptCount, LastError: item.LastError, CanRetry: item.State == "failed" || item.State == "bounced", RetryKey: uuid.NewString()})
	}
	return rows
}

func deliveryTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format("2006-01-02 15:04")
}

func retryDocumentDelivery(svc *delivery.Service, objectType string, w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	deliveryID, deliveryErr := strconv.ParseInt(chi.URLParam(r, "delivery_id"), 10, 64)
	if err != nil || deliveryErr != nil {
		http.NotFound(w, r)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	key, err := uuid.Parse(r.FormValue("idempotency_key"))
	if err != nil {
		http.Error(w, "valid idempotency UUID is required", http.StatusUnprocessableEntity)
		return
	}
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	history, err := svc.History(r.Context(), u.CompanyID, delivery.DocumentRef{Type: objectType, ID: id})
	if err != nil {
		internalServerError(w, r, "load delivery", err)
		return
	}
	found := false
	for _, item := range history {
		if item.ID == deliveryID {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	err = svc.ManualRetry(r.Context(), delivery.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}, deliveryID, r.FormValue("reason"), key)
	if err != nil {
		switch {
		case errors.Is(err, delivery.ErrForbidden):
			http.Error(w, "Forbidden", http.StatusForbidden)
		case errors.Is(err, delivery.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, delivery.ErrInvalid):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		default:
			internalServerError(w, r, "retry delivery", err)
		}
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%ss/%d?flash=Delivery+retry+queued", objectType, id), http.StatusSeeOther)
}

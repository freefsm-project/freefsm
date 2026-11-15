package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Email reconciliation owns a protocol separate from generic form admission, but
// uses the existing action identity, execution fence and atomic command owner.
func (h *integrationHandler) reconcileEmail(w http.ResponseWriter, r *http.Request) {
	h.emailAttempt(w, r, false)
}

func (h *integrationHandler) retryEmail(w http.ResponseWriter, r *http.Request) {
	h.emailAttempt(w, r, true)
}

func (h *integrationHandler) emailAttempt(w http.ResponseWriter, r *http.Request, retry bool) {
	id := chi.URLParam(r, "operation")
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil {
		http.Error(w, "Invalid action identity", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-FreeFSM-Action", id)
	w.Header().Set("X-FreeFSM-Receipt", "/save-actions/"+id)
	w.Header().Set("X-FreeFSM-Disposition", "unknown")
	ctx, release, err := h.control.EnterEmailTransition(r.Context())
	if err != nil {
		http.Error(w, "Email policy unavailable; keep this action identity", 503)
		return
	}
	defer release()
	actor := authorizationActor(r)
	if _, err := h.authority.Administrator(ctx, actor); err != nil {
		authorizationError(w, r, err)
		return
	}
	a := saveaction.Action{UserID: actor.UserID, ID: id, Path: authorization.EmailEnablePath, Atomic: true, NoChange: true, Outcome: &saveaction.Outcome{}}
	err = h.authority.Pool.QueryRow(ctx, `SELECT intent_hash,execution_review,execution_token::text FROM online_save_actions WHERE user_id=$1 AND operation_id=$2 AND request_path=$3 AND execution_token IS NOT NULL`, actor.UserID, id, a.Path).Scan(&a.IntentHash, &a.Revision, &a.ExecutionToken)
	if errors.Is(err, pgx.ErrNoRows) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"disposition": "unknown"})
		return
	}
	if err != nil {
		http.Error(w, "Save status unavailable; keep this action identity", 503)
		return
	}
	if retry {
		// The explicit button submits the reviewed revision and the reconciled
		// token. Receipt ownership alone never supplies a retry precondition.
		previous := r.FormValue("execution_token")
		if previous != a.ExecutionToken || r.FormValue("review") != a.Revision {
			http.Error(w, "Review changed; reconcile this same action again", 409)
			return
		}
		a.ExecutionToken = uuid.NewString()
		if err = h.authority.RetryEmailEnable(ctx, actor, a, previous); err == nil {
			err = h.authority.EnableEmail(saveaction.WithAction(ctx, a), actor)
		}
	} else {
		_, err = h.authority.ReconcileEmailEnable(ctx, actor, a)
	}
	if err != nil {
		var failure *authorization.TransactionFailure
		if errors.As(err, &failure) && !failure.RolledBack {
			http.Error(w, "enable_outcome_unknown: Check this same action again", 503)
		} else {
			authorizationError(w, r, err)
		}
		return
	}
	receipt, err := (saveaction.Store{Pool: h.authority.Pool}).Receipt(ctx, a)
	if err != nil {
		http.Error(w, "Save status unavailable; check this same action again", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-FreeFSM-Disposition", receipt.Disposition)
	_ = json.NewEncoder(w).Encode(struct {
		saveaction.Receipt
		Token  string `json:"execution_token"`
		Review string `json:"review"`
	}{receipt, a.ExecutionToken, a.Revision})
}

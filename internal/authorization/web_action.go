package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type WebAction struct{ ID, Revision string }
type WebReceipt struct {
	OperationID string `json:"operation_id"`
	Disposition string `json:"disposition"`
	EffectID    int64  `json:"effect_id"`
	Location    string `json:"location,omitempty"`
}
type CustomerValidationError struct{ Field, Message string }

func (e *CustomerValidationError) Error() string { return e.Message }

// Opaque revision covers all existing writers, including legacy ones which
// maintain updated_at. It is compared under the command's row lock.
func CustomerRevision(c *ent.Customer) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("customer:%d:%s", c.ID, c.UpdatedAt.UTC().Format(time.RFC3339Nano))))
	return hex.EncodeToString(sum[:])
}

func customerIntent(id int64, in CustomerInput, action WebAction) string {
	data, _ := json.Marshal(struct {
		ID       int64
		Input    CustomerInput
		Revision string
	}{id, in, action.Revision})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Receipt lookup deliberately does not require Customer View; it never contains
// names, submitted fields, comparison data or a sensitive response snapshot.
func (s *Service) WebReceipt(ctx context.Context, a Actor, id string) (WebReceipt, error) {
	r := WebReceipt{OperationID: id}
	if _, err := uuid.Parse(id); err != nil {
		return r, ErrNotFound
	}
	_, err := s.Principal(ctx, a)
	if err != nil {
		return r, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT disposition,effect_id FROM web_action_receipts WHERE user_id=$1 AND operation_id=$2`, a.UserID, id).Scan(&r.Disposition, &r.EffectID)
	if err == pgx.ErrNoRows {
		r.Disposition = "unknown"
		return r, nil
	}
	if err == nil && r.Disposition == "saved" {
		r.Location = fmt.Sprintf("/customers/%d", r.EffectID)
	}
	return r, err
}

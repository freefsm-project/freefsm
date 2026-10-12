// Package protectedaudit writes immutable evidence inside the caller's effect
// transaction. It neither authorizes commands nor starts/commits transactions.
package protectedaudit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ProductAction records the effective product authority used by a command.
// RoleID zero is reserved for an explicitly authorized transitional approver.
type ProductAction struct {
	ActorID, RoleID           int64
	Action, Scope, TargetType string
	TargetID                  int64
	Channel                   string
	Detail                    any
}

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func RecordProduct(ctx context.Context, tx Executor, event ProductAction) error {
	if event.ActorID <= 0 || event.TargetID <= 0 || event.Action == "" || event.Action == "admin_password_recovered" || event.TargetType == "" || event.Channel == "" || event.Channel == "host-local" || !validScope(event.Scope) {
		return errors.New("invalid product audit attribution")
	}
	data, err := json.Marshal(event.Detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO authorization_audit(actor_kind,actor_id,role_id,action,scope,target_type,target_id,channel,detail)
 VALUES('product-user',$1,nullif($2,0),$3,$4,$5,$6,$7,$8)`, event.ActorID, event.RoleID, event.Action, event.Scope, event.TargetType, event.TargetID, event.Channel, data)
	return err
}

type HostRecovery struct {
	HostUID                               string
	TargetUserID, TargetRoleID, CompanyID int64
}

// RecordRecoveryRequest attributes public link issuance honestly: knowing an
// email address is not authentication as the target Product User.
func RecordRecoveryRequest(ctx context.Context, tx Executor, userID int64) error {
	if userID <= 0 {
		return errors.New("invalid recovery target")
	}
	_, err := tx.Exec(ctx, `INSERT INTO authorization_audit(actor_kind,actor_id,role_id,action,scope,target_type,target_id,channel,detail) VALUES('anonymous-request',NULL,NULL,'user.recovery.request',NULL,'user',$1,'recovery','{"initiator":"public recovery request"}')`, userID)
	return err
}

// RecordHostRecovery is deliberately purpose-specific. Host authority can record
// recovery of a verified protected identity, never manufacture a product actor,
// Role grant, or arbitrary sealed action. No credential material is accepted.
func RecordHostRecovery(ctx context.Context, tx Executor, event HostRecovery) error {
	if strings.TrimSpace(event.HostUID) == "" || event.TargetUserID <= 0 || event.TargetRoleID <= 0 || event.CompanyID <= 0 {
		return errors.New("invalid host recovery audit attribution")
	}
	data, err := json.Marshal(struct {
		CompanyID    int64 `json:"company_id"`
		TargetRoleID int64 `json:"target_role_id"`
	}{event.CompanyID, event.TargetRoleID})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO authorization_audit(actor_kind,host_uid,actor_id,role_id,action,scope,target_type,target_id,channel,detail)
 VALUES('deployment-operator',$1,NULL,NULL,'admin_password_recovered',NULL,'user',$2,'host-local',$3)`, event.HostUID, event.TargetUserID, data)
	return err
}

func validScope(scope string) bool {
	switch scope {
	case "A", "J", "C", "O", "U", "S":
		return true
	}
	return false
}

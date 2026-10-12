// Package hostrecovery implements host-authorized recovery, not a product Role
// or an HTTP endpoint. Callers must hold instance operation and maintenance locks.
package hostrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// ResetExistingAdmin changes exactly one enabled, initialized Admin. All account
// effects and the host-attributed audit record commit together. No user is added,
// enabled, promoted, or assigned permissions by recovery.
func ResetExistingAdmin(ctx context.Context, pool *pgxpool.Pool, email, password, hostUID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return errors.New("cannot begin Admin recovery; check database availability")
	}
	defer tx.Rollback(context.Background())
	// Serialize policy/identity changes and credential issuers even when another
	// database client does not participate in filesystem admission.
	// Take the authorization mutex before table locks, matching Role/user writers.
	// Recovery must not race a reviewed cutover or a protected Role reassignment.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019); LOCK TABLE authorization_state, authorization_roles, company_settings IN SHARE MODE; LOCK TABLE users, sessions, password_reset_tokens IN EXCLUSIVE MODE`); err != nil {
		return errors.New("Admin recovery requires an initialized, migrated installation")
	}
	var id, companyID, roleID int64
	var role string
	var active, protected, ready bool
	if err = tx.QueryRow(ctx, `SELECT u.id,u.company_id,r.name,u.is_active,r.protected,s.ready,r.id FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id CROSS JOIN authorization_state s WHERE u.email=$1 FOR UPDATE OF u`, email).Scan(&id, &companyID, &role, &active, &protected, &ready, &roleID); err != nil {
		return errors.New("recovery target must be an existing enabled Administrator")
	}
	if !protected || !active || !ready {
		return errors.New("recovery target must be an existing enabled Administrator")
	}
	policy := &ent.CompanySettings{}
	err = tx.QueryRow(ctx, `SELECT password_min_length,password_require_uppercase,password_require_lowercase,password_require_digit,password_require_special FROM company_settings WHERE company_id=$1`, companyID).Scan(
		&policy.PasswordMinLength, &policy.PasswordRequireUppercase, &policy.PasswordRequireLowercase, &policy.PasswordRequireDigit, &policy.PasswordRequireSpecial)
	if err != nil {
		return errors.New("cannot read Admin password policy; complete company setup first")
	}
	if err := services.NewUserService(nil).ValidatePassword(password, policy); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("password must be no more than 72 bytes")
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$1,force_password_change=true,updated_at=NOW() WHERE id=$2`, string(hash), id); err != nil {
		return errors.New("cannot update Admin password")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
		return errors.New("cannot revoke Admin sessions")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1`, id); err != nil {
		return errors.New("cannot revoke Admin recovery links")
	}
	metadata, err := json.Marshal(map[string]string{"channel": "host-local", "actor": "deployment-operator", "host_uid": hostUID, "target_role": role, "outcome": "success"})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,NULL,'admin_password_recovered','user',$2,$3)`, companyID, id, string(metadata)); err != nil {
		return errors.New("cannot record Admin recovery audit; no account changes committed")
	}
	if err = protectedaudit.RecordHostRecovery(ctx, tx, protectedaudit.HostRecovery{HostUID: hostUID, TargetUserID: id, TargetRoleID: roleID, CompanyID: companyID}); err != nil {
		return errors.New("cannot record protected Admin recovery audit; no account changes committed")
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("Admin recovery commit was not confirmed; inspect account state before retrying")
	}
	return nil
}

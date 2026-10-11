package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AuthorizationInventory records observations, never proposed or historical grants.
// ReviewRequired is not a cutover approval: this utility cannot approve mappings.
type AuthorizationInventory struct {
	FormatVersion         int                     `json:"format_version"`
	Schema                string                  `json:"schema"`
	Migrations            []string                `json:"migrations"`
	Tables                []string                `json:"tables"`
	Users                 []AuthorizationIdentity `json:"users"`
	LegacyRoles           []LegacyRoleInventory   `json:"legacy_roles"`
	LegacyRoleConstraints []string                `json:"legacy_role_constraints"`
	RecordedGrantStorage  string                  `json:"recorded_grant_storage"`
	ReviewRequired        bool                    `json:"review_required"`
	ReviewReasons         []string                `json:"review_reasons"`
	Fingerprint           string                  `json:"fingerprint"`
}

type AuthorizationIdentity struct {
	ID              int64  `json:"id"`
	CompanyID       *int64 `json:"company_id"`
	Name            string `json:"name"`
	Email           string `json:"email"`
	LegacyRole      string `json:"legacy_role"`
	Enabled         bool   `json:"enabled"`
	PasswordPresent bool   `json:"password_present"`
}

// LegacyRoleInventory groups stored discriminator values, not Role identities.
// Legacy releases have no independently identified Role or grant relation.
type LegacyRoleInventory struct {
	Value   string  `json:"value"`
	UserIDs []int64 `json:"user_ids"`
}

// AuthorizationPreflight takes one repeatable, read-only PostgreSQL snapshot.
// It does not run migrations, seed Roles, validate passwords, or modify audit.
// PasswordPresent only records nonempty credential storage; it does not claim
// that the user can authenticate or is eligible to be the protected Administrator.
func (db *DB) AuthorizationPreflight(ctx context.Context) (AuthorizationInventory, error) {
	r := AuthorizationInventory{FormatVersion: 1, Migrations: []string{}, Tables: []string{}, Users: []AuthorizationIdentity{}, LegacyRoles: []LegacyRoleInventory{}, LegacyRoleConstraints: []string{}, ReviewReasons: []string{}, RecordedGrantStorage: "unknown"}
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AuthorizationInventory{}, fmt.Errorf("begin authorization inventory: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&r.Schema); err != nil {
		return AuthorizationInventory{}, err
	}
	rows, err := tx.Query(ctx, `SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if err != nil {
		return AuthorizationInventory{}, err
	}
	r.Tables, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return AuthorizationInventory{}, err
	}
	hasUsers, hasMigrations, unexpectedAuthority := false, false, false
	for _, table := range r.Tables {
		hasUsers = hasUsers || table == "users"
		hasMigrations = hasMigrations || table == "schema_migrations"
		lower := strings.ToLower(table)
		if strings.Contains(lower, "role") || strings.Contains(lower, "grant") || strings.Contains(lower, "permission") {
			unexpectedAuthority = true
			r.ReviewReasons = append(r.ReviewReasons, "unrecognized authorization storage requires explicit inspection: "+table)
		}
	}
	if hasMigrations {
		rows, err := tx.Query(ctx, `SELECT name FROM schema_migrations ORDER BY name`)
		if err != nil {
			return AuthorizationInventory{}, err
		}
		r.Migrations, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return AuthorizationInventory{}, err
		}
	}
	if !hasUsers {
		r.ReviewReasons = append(r.ReviewReasons, "users table absent; an uninitialized or unsupported database is not an upgrade inventory")
	} else {
		rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name='users'
			AND column_name NOT IN ('id','company_id','name','email','role','is_active','password_hash',
			'font_size','last_schedule_tab','last_schedule_period','force_password_change',
			'welcome_email_sent_at','onboarding_completed_at','created_at','updated_at') ORDER BY column_name`)
		if err != nil {
			return AuthorizationInventory{}, err
		}
		unknownColumns, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return AuthorizationInventory{}, err
		}
		for _, column := range unknownColumns {
			unexpectedAuthority = true
			r.ReviewReasons = append(r.ReviewReasons, "unrecognized user field requires explicit inspection: "+column)
		}
		rows, err = tx.Query(ctx, `SELECT id, company_id, name, email, role, is_active, length(password_hash)>0 FROM users ORDER BY id`)
		if err != nil {
			return AuthorizationInventory{}, fmt.Errorf("unsupported or inaccessible legacy users schema: %w", err)
		}
		r.Users, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuthorizationIdentity, error) {
			var u AuthorizationIdentity
			err := row.Scan(&u.ID, &u.CompanyID, &u.Name, &u.Email, &u.LegacyRole, &u.Enabled, &u.PasswordPresent)
			return u, err
		})
		if err != nil {
			return AuthorizationInventory{}, err
		}
		// Values are ordered by first user ID, preserving identifiers and labels
		// exactly. A label is never interpreted as a new permission or authority.
		roleIndex := map[string]int{}
		for _, u := range r.Users {
			i, ok := roleIndex[u.LegacyRole]
			if !ok {
				i = len(r.LegacyRoles)
				roleIndex[u.LegacyRole] = i
				r.LegacyRoles = append(r.LegacyRoles, LegacyRoleInventory{Value: u.LegacyRole})
			}
			r.LegacyRoles[i].UserIDs = append(r.LegacyRoles[i].UserIDs, u.ID)
		}
		rows, err = tx.Query(ctx, `SELECT pg_get_constraintdef(c.oid) FROM pg_catalog.pg_constraint c
			JOIN pg_catalog.pg_class t ON t.oid=c.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace
			WHERE n.nspname=current_schema() AND t.relname='users' AND c.contype='c'
			AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=t.oid AND a.attname='role' AND a.attnum=ANY(c.conkey))
			ORDER BY c.conname`)
		if err != nil {
			return AuthorizationInventory{}, err
		}
		r.LegacyRoleConstraints, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return AuthorizationInventory{}, err
		}
	}
	knownMigrations, err := legacyAuthorizationMigrationNames()
	if err != nil {
		return AuthorizationInventory{}, err
	}
	if !equalStrings(r.Migrations, knownMigrations) {
		r.ReviewReasons = append(r.ReviewReasons, "migration inventory differs from the supported legacy baseline; inspect before cutover")
	} else if hasUsers && !unexpectedAuthority {
		r.RecordedGrantStorage = "absent_in_supported_legacy_schema"
	}
	if len(r.Users) > 0 {
		r.ReviewReasons = append(r.ReviewReasons, "existing users require reviewed identity-to-Role mappings, protected Administrator selection, and explicit grants; labels are not grants")
	}
	r.ReviewRequired = len(r.ReviewReasons) > 0
	// The fingerprint binds only the exported observations, not credentials. It
	// is a review reference, not a commit-time revision or authentication proof.
	encoded, err := json.Marshal(r)
	if err != nil {
		return AuthorizationInventory{}, err
	}
	sum := sha256.Sum256(encoded)
	r.Fingerprint = hex.EncodeToString(sum[:])
	if err := tx.Commit(ctx); err != nil {
		return AuthorizationInventory{}, err
	}
	return r, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func legacyAuthorizationMigrationNames() ([]string, error) {
	files, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, file := range files {
		if !file.IsDir() && file.Name() <= "054_backup_activity.up.sql" && strings.HasSuffix(file.Name(), ".up.sql") {
			names = append(names, strings.TrimSuffix(file.Name(), ".up.sql"))
		}
	}
	return names, nil
}

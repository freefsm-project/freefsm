package saveaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var ErrStale = errors.New("This form has changed since it was opened. Review the current authorized values before revising or discarding your input.")

var ErrRevisionRequired = errors.New("A current form revision and save identity are required. Reload the authorized form and review before submitting.")

// RequireRevision is the existing-record command contract, not an optional
// transport hint. Call after current authority checks, under the command's
// transaction fence. The target path is chosen by the domain, never the client.
// Omission must not turn an optimistic update into an unconditional overwrite.
func RequireRevision(ctx context.Context, tx pgx.Tx, userID int64, path string) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok || a.ID == "" || a.UserID != userID || len(a.Revision) != sha256.Size*2 {
		return ErrRevisionRequired
	}
	if _, err := hex.DecodeString(a.Revision); err != nil {
		return ErrRevisionRequired
	}
	current, err := revision(ctx, tx, userID, path, true)
	if err != nil {
		return err
	}
	if current == "" || current != a.Revision {
		return ErrStale
	}
	return nil
}

type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Revision covers the editable parent, including legacy writers which may not
// maintain a numeric revision. Native domain revisions continue to validate
// children, workflow meanings, facets and selected document evidence themselves.
// Identifiers below are a closed server-side mapping, never user-provided SQL.
func Revision(ctx context.Context, q Querier, userID int64, path string) (string, error) {
	return revision(ctx, q, userID, path, false)
}

func revision(ctx context.Context, q Querier, userID int64, path string, lock bool) (string, error) {
	var query string
	var args []any
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "customers" && (parts[2] == "contacts" || parts[2] == "locations") {
		parts = []string{parts[2], parts[3]}
	}
	if path == "/preferences" {
		query = `SELECT jsonb_build_array(u.font_size,coalesce(p.theme,'system'))::text FROM users u LEFT JOIN user_presentation p ON p.user_id=u.id WHERE u.id=$1`
		if lock {
			query += ` FOR UPDATE OF u`
		}
		args = []any{userID}
	} else if len(parts) >= 2 {
		tables := map[string]string{"customers": "customers", "contacts": "customer_contacts", "locations": "locations", "items": "items", "jobs": "jobs", "estimates": "estimates", "invoices": "invoices", "projects": "projects", "assets": "assets", "tags": "tags", "asset-types": "asset_types", "users": "users", "time-entries": "time_entries"}
		table := tables[parts[0]]
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if table == "" || err != nil || id <= 0 {
			return "", nil
		}
		query = `SELECT to_jsonb(t)::text FROM ` + table + ` t WHERE id=$1`
		if table == "jobs" {
			query = `SELECT (to_jsonb(t)-ARRAY['updated_at','status_id','status_revision','tech_notes','notes_revision'])::text FROM jobs t WHERE id=$1`
		}
		if table == "time_entries" {
			// A version token must not fingerprint the hidden Job/GPS values.
			query = `SELECT jsonb_build_array(t.id,t.xmin::text,t.updated_at)::text FROM time_entries t WHERE id=$1`
		}
		if lock {
			query += ` FOR UPDATE`
		}
		args = []any{id}
	} else {
		return "", nil
	}
	var data string
	if err := q.QueryRow(ctx, query, args...).Scan(&data); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:]), nil
}

// Check runs under the domain's existing authority mutex, before its writes.
// Subsequent transactions in this same request must not compare against their
// own earlier committed change. Such requests remain explicitly multi-step.
func Check(ctx context.Context, tx pgx.Tx) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok || a.Revision == "" {
		return nil
	}
	if a.Outcome != nil && a.Outcome.ReadOnly.Load() {
		return nil
	}
	var committed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM online_save_effects WHERE user_id=$1 AND operation_id=$2)`, a.UserID, a.ID).Scan(&committed); err != nil {
		return err
	}
	if committed {
		return nil
	}
	current, err := revision(ctx, tx, a.UserID, a.Path, true)
	if err != nil {
		return err
	}
	if current != "" && current != a.Revision {
		return ErrStale
	}
	return nil
}

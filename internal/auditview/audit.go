// Package auditview exposes a closed evidence projection. It never joins old
// audit IDs to current people or emits arbitrary detail JSON or removed text.
package auditview

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrFilter = errors.New("invalid audit filters")

type Filters struct {
	Permission string `json:"permission"`
	Scope      string `json:"scope"`
	Channel    string `json:"channel"`
	Outcome    string `json:"outcome"`
	Role       string `json:"role"`
	Sort       string `json:"sort"`
}

func (f Filters) Validate() error {
	if f.Sort != "oldest" && f.Sort != "newest" {
		return ErrFilter
	}
	// Role names have no domain length limit; recorded names must remain filterable.
	for _, v := range []string{f.Permission, f.Channel, f.Outcome} {
		if len(v) > 200 {
			return ErrFilter
		}
	}
	switch f.Scope {
	case "", "A", "J", "C", "O", "U", "S":
	default:
		return ErrFilter
	}
	return nil
}
func (f Filters) Query() string {
	return url.Values{"permission": {f.Permission}, "scope": {f.Scope}, "channel": {f.Channel}, "outcome": {f.Outcome}, "role": {f.Role}, "sort": {f.Sort}}.Encode()
}

// These versioned columns contain recorded facts only. Empty means not recorded.
func Columns() []string {
	return []string{"audit_id", "accepted_at", "actor_kind", "actor_id", "actor_identity", "actor_name", "event_role_id", "event_role_identity", "event_role_name", "permission", "scope", "target_type", "target_id", "channel", "outcome", "host_uid", "initiator_id", "initiator_identity", "initiator_name", "device_name", "session_id", "operation_id", "occurred_at"}
}

type Row struct {
	ID     int64
	Values []string
}
type Page struct {
	Rows []Row
	AsOf time.Time
	Next int64
}

// ReadRows operates within the caller's authorized transaction/snapshot. The
// cursor bounds memory without weakening the all-matching export contract.
func ReadRows(ctx context.Context, tx pgx.Tx, f Filters, after int64, limit int, emit func(Row) error) error {
	if err := f.Validate(); err != nil {
		return err
	}
	order, comparison := "id DESC", "id<$6"
	if f.Sort == "oldest" {
		order, comparison = "id ASC", "id>$6"
	}
	query := `SELECT id,recorded_at,CASE WHEN channel='worker' THEN 'system' ELSE actor_kind END,
 CASE WHEN channel='worker' THEN '' ELSE coalesce(actor_id::text,'') END,
 CASE WHEN channel='worker' THEN '' ELSE coalesce(actor_identity::text,'') END,
 CASE WHEN channel='worker' THEN '' ELSE coalesce(actor_name,'') END,
 coalesce(role_id::text,''),coalesce(role_identity::text,''),coalesce(role_name,''),action,coalesce(scope,''),target_type,target_id::text,channel,outcome,coalesce(host_uid,''),
 CASE WHEN channel='worker' THEN coalesce(actor_id::text,'') ELSE '' END,
 CASE WHEN channel='worker' THEN coalesce(actor_identity::text,'') ELSE '' END,
 CASE WHEN channel='worker' THEN coalesce(actor_name,'') ELSE '' END,
 coalesce(device_name,CASE WHEN jsonb_typeof(detail->'device_name')='string' THEN detail->>'device_name' END,''),
 coalesce(session_id::text,CASE WHEN pg_input_is_valid(detail->>'session_id','bigint') THEN (detail->>'session_id')::bigint::text END,''),
 coalesce(operation_id::text,CASE WHEN pg_input_is_valid(detail->>'operation_id','uuid') THEN (detail->>'operation_id')::uuid::text END,''),
 coalesce(occurred_at,authorization_audit_instant(detail->>'occurred_at'))
 FROM authorization_audit WHERE ($1='' OR action=$1) AND ($2='' OR scope=$2) AND ($3='' OR channel=$3) AND ($4='' OR outcome=$4) AND ($5='' OR role_name=$5) AND ($6::bigint=0 OR ` + comparison + `) ORDER BY ` + order
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	if _, err := tx.Exec(ctx, "DECLARE protected_audit_rows NO SCROLL CURSOR FOR "+query, f.Permission, f.Scope, f.Channel, f.Outcome, f.Role, after); err != nil {
		return err
	}
	for {
		rows, err := tx.Query(ctx, "FETCH FORWARD 128 FROM protected_audit_rows")
		if err != nil {
			return err
		}
		batch := []Row{}
		for rows.Next() {
			r := Row{Values: make([]string, len(Columns()))}
			var accepted time.Time
			var occurred *time.Time
			dest := []any{&r.ID, &accepted}
			for i := 2; i < 22; i++ {
				dest = append(dest, &r.Values[i])
			}
			dest = append(dest, &occurred)
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return err
			}
			r.Values[0] = strconv.FormatInt(r.ID, 10)
			r.Values[1] = accepted.UTC().Format(time.RFC3339Nano)
			if occurred != nil {
				r.Values[22] = occurred.UTC().Format(time.RFC3339Nano)
			}
			batch = append(batch, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if err := emit(r); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, "CLOSE protected_audit_rows")
	return err
}

func Browse(ctx context.Context, pool *pgxpool.Pool, a authorization.Actor, f Filters, after int64, size int) (Page, error) {
	out := Page{Rows: []Row{}}
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	if after < 0 || (size != 25 && size != 50 && size != 100) {
		return out, ErrFilter
	}
	err := authorization.New(pool).WithinSnapshotPermission(ctx, a, "audit.view", authorization.Sealed, func(tx pgx.Tx, p authorization.Principal) error {
		if err := tx.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&out.AsOf); err != nil {
			return err
		}
		if err := ReadRows(ctx, tx, f, after, size+1, func(row Row) error { out.Rows = append(out.Rows, row); return nil }); err != nil {
			return err
		}
		if len(out.Rows) > size {
			out.Rows = out.Rows[:size]
			out.Next = out.Rows[size-1].ID
		}
		return protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: a.UserID, RoleID: p.RoleID, Action: "audit.view", Scope: "S", TargetType: "instance", TargetID: p.CompanyID, Channel: "web", Detail: map[string]any{"filters": f, "after": after, "size": size}})
	})
	if err != nil {
		return Page{}, err
	}
	return out, nil
}

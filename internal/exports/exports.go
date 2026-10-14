// Package exports owns durable requester-only CSV jobs. Domain adapters own
// snapshot selection and whole-snapshot current row/field authority.
package exports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConflict    = errors.New("export intent changed; review a new export")
	ErrNotReady    = errors.New("export is not Ready; check its durable status")
	ErrUnavailable = errors.New("export unavailable; generate a new export")
)

// Definition is versioned: never change columns or reference meaning for an
// existing Kind. Recheck must reject the ENTIRE snapshot on any lost facet/row.
type Definition interface {
	Kind() string
	Permission() string
	Scope() authorization.Scope
	Columns() []string
	Validate(json.RawMessage) error
	Authorize(authorization.Principal, json.RawMessage) error
	Snapshot(context.Context, pgx.Tx, authorization.Principal, json.RawMessage, func(Record) error) error
	Recheck(context.Context, pgx.Tx, authorization.Principal, json.RawMessage) error
}
type Record struct {
	Values    []string
	Reference json.RawMessage
}

// Domains with selected record inputs authorize those inputs under the same
// fence at acceptance, generation, status and download, even for empty output.
type selectionAuthorizer interface {
	AuthorizeSelection(context.Context, pgx.Tx, authorization.Principal, json.RawMessage) error
}

func authorizeDefinition(ctx context.Context, tx pgx.Tx, p authorization.Principal, d Definition, options json.RawMessage) error {
	if err := d.Authorize(p, options); err != nil {
		return err
	}
	if selected, ok := d.(selectionAuthorizer); ok {
		return selected.AuthorizeSelection(ctx, tx, p, options)
	}
	return nil
}

type Service struct {
	Pool        *pgxpool.Pool
	definitions map[string]Definition
}

func New(pool *pgxpool.Pool, definitions ...Definition) *Service {
	s := &Service{Pool: pool, definitions: map[string]Definition{}}
	for _, d := range definitions {
		if _, exists := s.definitions[d.Kind()]; exists {
			panic("duplicate export definition")
		}
		s.definitions[d.Kind()] = d
	}
	return s
}

type Status struct {
	ID         int64      `json:"id"`
	State      string     `json:"state"`
	AcceptedAt time.Time  `json:"accepted_at"`
	AsOf       *time.Time `json:"as_of,omitempty"`
	ReadyAt    *time.Time `json:"ready_at,omitempty"`
	RowCount   *int64     `json:"row_count,omitempty"`
	Failure    string     `json:"failure,omitempty"`
	Blocked    bool       `json:"blocked"`
	Expired    bool       `json:"expired"`
}

// List returns only requester-owned nonsensitive receipts, with keyset paging.
func (s *Service) List(ctx context.Context, a authorization.Actor, before int64) ([]Status, error) {
	p, err := authorization.New(s.Pool).Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,state,accepted_at FROM export_jobs WHERE requester_id=$1 AND company_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT 50`, a.UserID, p.CompanyID, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Status{}
	for rows.Next() {
		var status Status
		if err := rows.Scan(&status.ID, &status.State, &status.AcceptedAt); err != nil {
			return nil, err
		}
		result = append(result, status)
	}
	return result, rows.Err()
}

func (s *Service) Accept(ctx context.Context, actor authorization.Actor, operation, kind string, options json.RawMessage) (int64, error) {
	d, ok := s.definitions[kind]
	if !ok {
		return 0, ErrConflict
	}
	if _, err := uuid.Parse(operation); err != nil {
		return 0, ErrConflict
	}
	if err := d.Validate(options); err != nil {
		return 0, err
	}
	var id int64
	err := authorization.New(s.Pool).WithinPermission(ctx, actor, d.Permission(), d.Scope(), func(tx pgx.Tx, p authorization.Principal) error {
		if err := authorizeDefinition(ctx, tx, p, d, options); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `SELECT id,kind=$3 AND options=$4::jsonb FROM export_jobs WHERE requester_id=$1 AND operation_id=$2`, actor.UserID, operation, kind, options).Scan(&id, &same)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO export_jobs(requester_id,company_id,operation_id,kind,options) VALUES($1,$2,$3,$4,$5) RETURNING id`, actor.UserID, p.CompanyID, operation, kind, options).Scan(&id); err != nil {
			return err
		}
		return exportAudit(ctx, tx, actor, p, d, id, "accepted")
	})
	return id, err
}
func exportAudit(ctx context.Context, tx pgx.Tx, a authorization.Actor, p authorization.Principal, d Definition, id int64, event string) error {
	channel := a.Channel
	if channel == "" {
		channel = "web"
	}
	detail := map[string]any{"event": event, "kind": d.Kind(), "initiator_id": a.UserID, "executor": "requester"}
	if channel == "worker" {
		detail["executor"] = "system"
	}
	return protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: a.UserID, RoleID: p.RoleID, Action: d.Permission(), Scope: string(d.Scope()), TargetType: "export", TargetID: id, Channel: channel, Detail: detail})
}
func (s *Service) Status(ctx context.Context, a authorization.Actor, id int64) (Status, error) {
	var out Status
	// Status receipts intentionally retain only nonsensitive disposition after
	// authority loss. No filters, counts, as-of, or source identities are exposed.
	err := authorization.New(s.Pool).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		var kind string
		var options json.RawMessage
		err := tx.QueryRow(ctx, `SELECT id,state,accepted_at,failure,blocked,coalesce(clock_timestamp()>=ready_at+interval '24 hours',false),kind,options FROM export_jobs WHERE id=$1 AND requester_id=$2 AND company_id=$3`, id, a.UserID, p.CompanyID).Scan(&out.ID, &out.State, &out.AcceptedAt, &out.Failure, &out.Blocked, &out.Expired, &kind, &options)
		if errors.Is(err, pgx.ErrNoRows) {
			return authorization.ErrNotFound
		}
		if err != nil {
			return err
		}
		if out.State != "Ready" || out.Blocked || out.Expired {
			return nil
		}
		d, ok := s.definitions[kind]
		block := func() error {
			out.Blocked = true
			_, err := tx.Exec(ctx, `UPDATE export_jobs SET blocked=true WHERE id=$1`, id)
			return err
		}
		if !ok || !p.Allows(d.Permission(), d.Scope()) {
			return block()
		}
		if err := authorizeDefinition(ctx, tx, p, d, options); err != nil {
			if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
				return block()
			}
			return err
		}
		var count int64
		var asOf, ready time.Time
		if err := tx.QueryRow(ctx, `SELECT row_count,as_of,ready_at FROM export_jobs WHERE id=$1`, id).Scan(&count, &asOf, &ready); err != nil {
			return err
		}
		for ordinal := int64(1); ordinal <= count; ordinal++ {
			var ref json.RawMessage
			if err := tx.QueryRow(ctx, `SELECT reference FROM export_records WHERE export_id=$1 AND ordinal=$2`, id, ordinal).Scan(&ref); err != nil {
				return err
			}
			if err := d.Recheck(ctx, tx, p, ref); err != nil {
				if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
					return block()
				}
				return err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()>=ready_at+interval '24 hours' FROM export_jobs WHERE id=$1`, id).Scan(&out.Expired); err != nil {
			return err
		}
		if out.Expired {
			return nil
		}
		out.RowCount = &count
		out.AsOf = &asOf
		out.ReadyAt = &ready
		return nil
	})
	return out, err
}

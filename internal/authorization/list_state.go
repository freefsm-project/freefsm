package authorization

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"

	"github.com/freefsm-project/freefsm/internal/liststate"
	"github.com/jackc/pgx/v5"
)

type ListState struct {
	Values   url.Values
	Revision int64
}

func listStateKey(key string) bool {
	switch key {
	case "search", "customers", "contacts", "locations", "assets", "jobs",
		"projects", "estimates", "invoices", "leases", "items":
		return true
	}
	return false
}

func (s *Service) withinListState(ctx context.Context, actor Actor, key string, fn func(pgx.Tx, Principal) error) error {
	if actor.Channel != "" && actor.Channel != "web" {
		return ErrForbidden
	}
	if !listStateKey(key) {
		return fmt.Errorf("%w: unknown list", ErrConflict)
	}
	if s == nil || s.Pool == nil {
		return ErrUnavailable
	}
	return s.WithinActor(ctx, actor, fn)
}

// LoadListState freshly resolves the current user/company and session under the
// authority fence. Presentation is intrinsic to the actor; saved filters never
// confer the authority needed to execute them.
func (s *Service) LoadListState(ctx context.Context, actor Actor, key string) (ListState, error) {
	result := ListState{Values: url.Values{}}
	err := s.withinListState(ctx, actor, key, func(tx pgx.Tx, p Principal) error {
		var encoded string
		err := tx.QueryRow(ctx, `SELECT values_query,revision FROM list_states WHERE company_id=$1 AND user_id=$2 AND list_key=$3`, p.CompanyID, p.UserID, key).Scan(&encoded, &result.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.Values, err = url.ParseQuery(encoded)
		if err != nil {
			return fmt.Errorf("%w: malformed stored list state", ErrConflict)
		}
		if err := liststate.Bounded(result.Values); err != nil {
			return fmt.Errorf("%w: invalid stored list state", ErrConflict)
		}
		return nil
	})
	if err != nil {
		return ListState{}, err
	}
	return result, nil
}

func (s *Service) SaveListState(ctx context.Context, actor Actor, key string, values url.Values, expected int64) (ListState, error) {
	return s.writeListState(ctx, actor, key, liststate.Persistent(values), expected, nil)
}

// SaveAuthorizedListState validates the selected query's current authority and
// persists its presentation under ONE authority fence and transaction. Browser
// state adapters must use this operation: a successful earlier read alone does
// not authorize a later preference commit.
//
// validate must use the supplied transaction and fresh Principal, including
// independent archive/facet/relationship checks. It must not invoke public
// service reads that open a nested WithinActor transaction.
func (s *Service) SaveAuthorizedListState(ctx context.Context, actor Actor, key string, values url.Values, expected int64, validate func(pgx.Tx, Principal) error) (ListState, error) {
	if validate == nil {
		return ListState{}, fmt.Errorf("%w: list authority validator required", ErrForbidden)
	}
	return s.writeListState(ctx, actor, key, liststate.Persistent(values), expected, validate)
}

// ResetListState retains the incremented revision even for an absent row.
// An already-admitted save with an old revision cannot resurrect cleared state.
func (s *Service) ResetListState(ctx context.Context, actor Actor, key string, expected int64) (ListState, error) {
	return s.writeListState(ctx, actor, key, url.Values{}, expected, nil)
}

func (s *Service) writeListState(ctx context.Context, actor Actor, key string, values url.Values, expected int64, validate func(pgx.Tx, Principal) error) (ListState, error) {
	var result ListState
	err := s.withinListState(ctx, actor, key, func(tx pgx.Tx, p Principal) error {
		if validate != nil {
			if err := validate(tx, p); err != nil {
				return err
			}
		}
		if expected < 0 || expected == math.MaxInt64 {
			return fmt.Errorf("%w: invalid list revision", ErrConflict)
		}
		if err := liststate.Bounded(values); err != nil {
			return fmt.Errorf("%w: invalid list state", ErrConflict)
		}
		if q, ok := values["q"]; ok {
			if err := liststate.Text(500)(q); err != nil {
				return fmt.Errorf("%w: invalid query text", ErrConflict)
			}
		}
		// Do not use Canonical here: an empty Reset tombstone must stay empty.
		encoded := values.Encode()
		var revision int64
		var err error
		if expected == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO list_states(company_id,user_id,list_key,values_query)
				VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING RETURNING revision`,
				p.CompanyID, p.UserID, key, encoded).Scan(&revision)
		} else {
			err = tx.QueryRow(ctx, `UPDATE list_states SET values_query=$4,revision=revision+1
				WHERE company_id=$1 AND user_id=$2 AND list_key=$3 AND revision=$5 RETURNING revision`,
				p.CompanyID, p.UserID, key, encoded, expected).Scan(&revision)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: stale list revision", ErrConflict)
		}
		if err != nil {
			return err
		}
		result = ListState{Values: values, Revision: revision}
		// DISC-06: browsing and intrinsic presentation changes are not business
		// activity. In particular, never emit searches into the audit stream.
		return nil
	})
	if err != nil {
		return ListState{}, err
	}
	return result, nil
}

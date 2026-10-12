package statusflow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

var ErrStaleIntent = errors.New("status intent changed; explicitly reselect the target")

type Intent struct {
	StatusID        int64  `json:"status_id"`
	Revision        string `json:"status_revision"`
	MeaningRevision string `json:"meaning_revision"`
}

type StatusPresentation struct {
	ID       int64       `json:"id"`
	Name     string      `json:"name"`
	Color    string      `json:"color"`
	Category CategoryKey `json:"category"`
	Order    int         `json:"order"`
}

type IntentChoice struct {
	Status StatusPresentation `json:"status"`
	Intent Intent             `json:"intent"`
}

// DiscoverIntents authorizes the actual parent and returns presentation and both
// intent revisions from one SQL snapshot. The authority fence and parent locks
// also serialize configuration changes, revocations and assignment loss.
func (s *Service) DiscoverIntents(ctx context.Context, a Actor, typ ObjectType, id int64) ([]IntentChoice, error) {
	if !validObjectType(typ) {
		return nil, ErrWrongType
	}
	choices := []IntentChoice{}
	err := s.command(ctx, a, typ, id, "", func(tx pgx.Tx, _ Actor) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT s.id,s.name,s.color,s.category_key,s.category_order,o.status_revision::text,s.meaning_revision::text
		FROM %ss o JOIN status_workflows w ON w.company_id=o.company_id AND w.object_type=$3 JOIN statuses s ON s.workflow_id=w.id AND s.company_id=o.company_id
		WHERE o.id=$1 AND o.company_id=$2 AND o.deleted_at IS NULL`, typ), id, a.CompanyID, typ)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var choice IntentChoice
			if err = rows.Scan(&choice.Status.ID, &choice.Status.Name, &choice.Status.Color, &choice.Status.Category, &choice.Status.Order, &choice.Intent.Revision, &choice.Intent.MeaningRevision); err != nil {
				return err
			}
			choice.Intent.StatusID = choice.Status.ID
			choices = append(choices, choice)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(choices) == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	order := map[CategoryKey]int{}
	for i, category := range Categories {
		order[category.Key] = i
	}
	slices.SortFunc(choices, func(a, b IntentChoice) int {
		if n := cmp.Compare(order[a.Status.Category], order[b.Status.Category]); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Status.Order, b.Status.Order); n != 0 {
			return n
		}
		return cmp.Compare(a.Status.ID, b.Status.ID)
	})
	return choices, nil
}

// ObserveIntent is a snapshot for an already authorized record. Opaque revisions
// are independent of labels, ordering and unrelated record fields.
func (s *Service) ObserveIntent(ctx context.Context, company int64, typ ObjectType, id, target int64) (Intent, error) {
	out := Intent{StatusID: target}
	if !validObjectType(typ) {
		return out, ErrWrongType
	}
	err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT o.status_revision::text,s.meaning_revision::text FROM %ss o JOIN statuses s ON s.id=$3 JOIN status_workflows w ON w.id=s.workflow_id WHERE o.id=$1 AND o.company_id=$2 AND w.company_id=$2 AND w.object_type=$4`, typ), id, company, target, typ).Scan(&out.Revision, &out.MeaningRevision)
	return out, mapNoRows(err)
}

func (s *Service) ApplyIntent(ctx context.Context, a Actor, typ ObjectType, id int64, intent Intent) error {
	if !validObjectType(typ) {
		return ErrWrongType
	}
	if intent.Revision == "" || intent.MeaningRevision == "" {
		return ErrStaleIntent
	}
	return s.command(ctx, a, typ, id, string(typ)+".status", func(tx pgx.Tx, a Actor) error {
		var current *int64
		var revision, meaning string
		err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT o.status_id,o.status_revision::text,s.meaning_revision::text FROM %ss o JOIN statuses s ON s.id=$3 JOIN status_workflows w ON w.id=s.workflow_id WHERE o.id=$1 AND o.company_id=$2 AND o.deleted_at IS NULL AND w.company_id=$2 AND w.object_type=$4 FOR UPDATE OF o,s`, typ), id, a.CompanyID, intent.StatusID, typ).Scan(&current, &revision, &meaning)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleIntent
		}
		if err != nil {
			return err
		}
		if meaning != intent.MeaningRevision {
			return ErrStaleIntent
		}
		if current == nil || *current != intent.StatusID {
			if revision != intent.Revision {
				return ErrStaleIntent
			}
		}
		return s.transitionTx(ctx, tx, a, typ, id, intent.StatusID)
	})
}

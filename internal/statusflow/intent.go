package statusflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrStaleIntent = errors.New("status intent changed; explicitly reselect the target")

type Intent struct {
	StatusID        int64  `json:"status_id"`
	Revision        string `json:"status_revision"`
	MeaningRevision string `json:"meaning_revision"`
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
	return s.command(ctx, a, typ, string(typ)+".status", func(tx pgx.Tx, a Actor) error {
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

package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
	"slices"
)

type JobContactChoice struct {
	ID                 int64
	Name               string
	Selected, Archived bool
}

func (s *Service) JobContacts(ctx context.Context, a Actor, id int64) ([]JobContactChoice, error) {
	var choices []JobContactChoice
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "", true); err != nil {
			return err
		}
		if !p.Allows("job.relations.edit", All) {
			return ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT c.id,CASE WHEN $2 THEN trim(c.first_name||' '||c.last_name) ELSE 'Retain current Contact' END,jc.contact_id IS NOT NULL,c.deleted_at IS NOT NULL FROM jobs j JOIN customer_contacts c ON c.customer_id=j.customer_id AND c.company_id=j.company_id LEFT JOIN job_contacts jc ON jc.job_id=j.id AND jc.contact_id=c.id WHERE j.id=$1 AND (c.deleted_at IS NULL OR jc.contact_id IS NOT NULL) AND ($2 OR jc.contact_id IS NOT NULL) ORDER BY c.id`, id, p.Allows("contact.view", All))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c JobContactChoice
			if err := rows.Scan(&c.ID, &c.Name, &c.Selected, &c.Archived); err != nil {
				return err
			}
			choices = append(choices, c)
		}
		return rows.Err()
	})
	return choices, err
}

// SetJobContacts is a relationship action, independent of planning Edit and
// details requiredness. It never changes Status or derives links from primaries.
func (s *Service) SetJobContacts(ctx context.Context, a Actor, id int64, ids []int64) error {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		if _, err := parentAccess(ctx, tx, p, "job", id, "job.relations.edit", true); err != nil {
			return err
		}
		var customerID int64
		if err := tx.QueryRow(ctx, `SELECT customer_id FROM jobs WHERE id=$1`, id).Scan(&customerID); err != nil {
			return err
		}
		active, err := customerLifecycleState(ctx, tx, p, customerID, true)
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
		var old []int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(contact_id ORDER BY contact_id),'{}') FROM job_contacts WHERE job_id=$1`, id).Scan(&old); err != nil {
			return err
		}
		retained := map[int64]bool{}
		for _, v := range old {
			retained[v] = true
		}
		for _, v := range ids {
			if retained[v] {
				continue
			}
			if !p.Allows("contact.view", All) {
				return ErrNotFound
			}
			var active bool
			err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customer_contacts WHERE id=$1 AND company_id=$2 AND customer_id=$3 FOR UPDATE`, v, p.CompanyID, customerID).Scan(&active)
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if !active {
				return ErrConflict
			}
		}
		// Retain the recorded singular compatibility link only while selected;
		// never designate an arbitrary Contact as primary or historical first.
		if _, err := tx.Exec(ctx, `UPDATE jobs SET customer_contact_id=CASE WHEN customer_contact_id=ANY($2::bigint[]) THEN customer_contact_id ELSE NULL END,updated_at=clock_timestamp() WHERE id=$1`, id, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM job_contacts WHERE job_id=$1 AND NOT(contact_id=ANY($2::bigint[]))`, id, ids); err != nil {
			return err
		}
		for _, v := range ids {
			if _, err := tx.Exec(ctx, `INSERT INTO job_contacts VALUES($1,$2) ON CONFLICT DO NOTHING`, id, v); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, "job.relations.edit", All, "job", id, map[string]any{"contacts_before": old, "contacts_after": ids})
	})
}

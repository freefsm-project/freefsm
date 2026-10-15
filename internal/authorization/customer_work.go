package authorization

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type CustomerWork struct {
	ID                  int64
	Kind, Title, Status string
	CreatedAt           time.Time
	Archived            bool
}

// CustomerRelatedWork is a direct union: a Job's Project and documents are not
// traversed. Permission and lifecycle predicates run before ranking or counts.
func (s *Service) CustomerRelatedWork(ctx context.Context, a Actor, customerID int64) ([]CustomerWork, error) {
	return s.CustomerWorkCollection(ctx, a, customerID, "", false)
}

// CustomerWorkCollection fences current authority and lifecycle through the
// direct-union read. Archived rows require explicit opt-in AND independent View.
func (s *Service) CustomerWorkCollection(ctx context.Context, a Actor, customerID int64, kind string, archived bool) ([]CustomerWork, error) {
	result := []CustomerWork{}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if kind == "" && !p.Allows("project.view", All) && !p.Allows("job.view", All) && !p.Allows("job.view", Assigned) && !p.Allows("estimate.view", All) && !p.Allows("estimate.view", Assigned) && !p.Allows("invoice.view", All) && !p.Allows("invoice.view", Assigned) {
			return ErrNotFound
		}
		if kind != "" {
			switch kind {
			case "project", "job", "estimate", "invoice":
			default:
				return ErrNotFound
			}
			if !p.Allows(kind+".view", All) && !p.Allows(kind+".view", Assigned) {
				return ErrNotFound
			}
			if archived && !p.Allows(kind+".archived.view", All) {
				return ErrForbidden
			}
		}
		if _, err := childCustomer(ctx, tx, p, customerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,kind,title,status,created_at,archived FROM (
 SELECT p.id,'project' kind,p.name title,coalesce(s.name,'') status,p.created_at,p.deleted_at IS NOT NULL archived FROM projects p LEFT JOIN statuses s ON s.id=p.status_id WHERE p.customer_id=$1 AND p.company_id=$2 AND (p.deleted_at IS NULL OR $11) AND $4
 UNION ALL
 SELECT j.id,'job',j.job_type,coalesce(s.name,''),j.created_at,j.deleted_at IS NOT NULL FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id WHERE j.customer_id=$1 AND j.company_id=$2 AND (j.deleted_at IS NULL OR $12) AND ($5 OR ($6 AND j.deleted_at IS NULL AND EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$3)))
 UNION ALL
 SELECT e.id,'estimate',e.title,coalesce(s.name,''),e.created_at,e.deleted_at IS NOT NULL FROM estimates e LEFT JOIN statuses s ON s.id=e.status_id WHERE e.customer_id=$1 AND e.company_id=$2 AND (e.deleted_at IS NULL OR $13) AND e.conversion_hidden_at IS NULL AND ($7 OR ($8 AND EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=e.job_id AND j.company_id=$2 AND j.customer_id=$1 AND j.deleted_at IS NULL AND a.user_id=$3)))
 UNION ALL
 SELECT i.id,'invoice',i.title,coalesce(s.name,''),i.created_at,i.deleted_at IS NOT NULL FROM invoices i LEFT JOIN invoice_effective_status es ON es.invoice_id=i.id LEFT JOIN statuses s ON s.id=es.status_id WHERE i.customer_id=$1 AND i.company_id=$2 AND (i.deleted_at IS NULL OR $14) AND i.conversion_hidden_at IS NULL AND ($9 OR ($10 AND EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=i.job_id AND j.company_id=$2 AND j.customer_id=$1 AND j.deleted_at IS NULL AND a.user_id=$3)))
 ) work WHERE ($15='' OR kind=$15) ORDER BY created_at DESC,kind,id DESC`, customerID, p.CompanyID, a.UserID, p.Allows("project.view", All), p.Allows("job.view", All), p.Allows("job.view", Assigned), p.Allows("estimate.view", All), p.Allows("estimate.view", Assigned), p.Allows("invoice.view", All), p.Allows("invoice.view", Assigned), archived && p.Allows("project.archived.view", All), archived && p.Allows("job.archived.view", All), archived && p.Allows("estimate.archived.view", All), archived && p.Allows("invoice.archived.view", All), kind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w CustomerWork
			if err = rows.Scan(&w.ID, &w.Kind, &w.Title, &w.Status, &w.CreatedAt, &w.Archived); err != nil {
				return err
			}
			result = append(result, w)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

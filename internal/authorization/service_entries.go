package authorization

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ServiceEntryInput struct {
	LinkID           int64   `json:"link_id"`
	ServiceDate      string  `json:"service_date"`
	Summary          string  `json:"summary"`
	ResponsibleUsers []int64 `json:"responsible_user_ids"`
}

type ServiceEntry struct {
	JobID            int64     `json:"-"`
	ResponsibleUsers []int64   `json:"-"`
	ID               int64     `json:"id"`
	Revision         string    `json:"revision"`
	ServiceDate      string    `json:"service_date"`
	Summary          string    `json:"summary"`
	JobTitle         string    `json:"job_title"`
	ResponsibleNames []string  `json:"responsible_names"`
	RevisedAt        time.Time `json:"revised_at"`
}

type ServiceEntryParticipant struct {
	ID   int64
	Name string
}
type ServiceEntryPage struct {
	LinkID               int64
	Entries              []ServiceEntry
	Participants         []ServiceEntryParticipant
	CanCreate, CanRevise bool
}

func (s *Service) ServiceEntryPage(ctx context.Context, a Actor, jobID, assetID int64) (ServiceEntryPage, error) {
	var out ServiceEntryPage
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		scope, link, customer, _, err := serviceEntryRelationship(ctx, tx, p, a, jobID, assetID, "view")
		if err != nil {
			return err
		}
		out.Entries, err = serviceEntryHistory(ctx, tx, assetID, customer)
		out.LinkID = link
		if err != nil {
			return err
		}
		out.CanCreate = p.Allows("service_entry.create", scope)
		out.CanRevise = p.Allows("service_entry.revise", scope)
		rows, err := tx.Query(ctx, `SELECT id,name FROM users u WHERE company_id=$1 AND (id=$2 OR EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$3 AND user_id=u.id) OR $4) ORDER BY name,id`, p.CompanyID, a.UserID, jobID, p.Allows("directory.view", All))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var user ServiceEntryParticipant
			if err = rows.Scan(&user.ID, &user.Name); err != nil {
				return err
			}
			out.Participants = append(out.Participants, user)
		}
		return rows.Err()
	})
	return out, err
}

// serviceEntryRelationship checks the exact operational link, never the legacy
// singleton pointer or a later return to the same Customer. All writers share
// the authority fence with transfer, archive, assignments and Customer changes.
func serviceEntryRelationship(ctx context.Context, tx pgx.Tx, p Principal, a Actor, jobID, assetID int64, action string) (Scope, int64, int64, int64, error) {
	var active, assigned bool
	var customer int64
	err := tx.QueryRow(ctx, `SELECT customer_id,deleted_at IS NULL,EXISTS(SELECT 1 FROM job_assignments WHERE job_id=j.id AND user_id=$2) FROM jobs j WHERE id=$1 AND company_id=$3 FOR UPDATE`, jobID, a.UserID, p.CompanyID).Scan(&customer, &active, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, 0, 0, ErrNotFound
	}
	if err != nil {
		return "", 0, 0, 0, err
	}
	scope := All
	if assigned && active {
		scope = Assigned
	}
	if !p.Allows("job.view", scope) || !p.Allows("service_entry.view", scope) {
		return "", 0, 0, 0, ErrNotFound
	}
	if !p.Allows("asset.view", All) && !(assigned && active && p.Allows("asset.view", Context)) {
		return "", 0, 0, 0, ErrNotFound
	}
	if !active {
		return "", 0, 0, 0, ErrConflict
	}
	if !p.Allows("service_entry."+action, scope) {
		return "", 0, 0, 0, ErrForbidden
	}
	var link, event int64
	err = tx.QueryRow(ctx, `SELECT l.id,l.association_event_id FROM job_asset_links l JOIN assets a ON a.id=l.asset_id WHERE l.job_id=$1 AND l.asset_id=$2 AND l.customer_id=$3 AND l.operational AND l.unlinked_at IS NULL AND a.customer_id=$3 AND a.company_id=$4 AND a.deleted_at IS NULL FOR UPDATE OF l,a`, jobID, assetID, customer, p.CompanyID).Scan(&link, &event)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, 0, 0, ErrConflict
	}
	return scope, link, customer, event, err
}

func applyServiceEntry(ctx context.Context, tx pgx.Tx, p Principal, a Actor, jobID, assetID, id int64, predecessor string, in ServiceEntryInput, operationID string, occurred *time.Time) (int64, error) {
	action := "create"
	if id != 0 {
		action = "revise"
	}
	err := func() error {
		scope, link, customer, event, err := serviceEntryRelationship(ctx, tx, p, a, jobID, assetID, action)
		if err != nil {
			return err
		}
		if in.LinkID != link {
			return ErrConflict
		}
		if id != 0 {
			if _, err := uuid.Parse(predecessor); err != nil {
				return ErrConflict
			}
			var originalLink int64
			var current string
			err := tx.QueryRow(ctx, `SELECT e.link_id,r.id::text FROM asset_service_entries e JOIN asset_service_revisions r ON r.entry_id=e.id WHERE e.id=$1 AND e.job_id=$2 AND e.asset_id=$3 AND e.customer_id=$4 AND NOT EXISTS(SELECT 1 FROM asset_service_revisions next WHERE next.predecessor=r.id) FOR UPDATE OF e`, id, jobID, assetID, customer).Scan(&originalLink, &current)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if originalLink != link || current != predecessor {
				return ErrConflict
			}
		}
		date, err := time.Parse("2006-01-02", in.ServiceDate)
		if err != nil || strings.TrimSpace(in.Summary) == "" || len(in.ResponsibleUsers) == 0 || occurred != nil && occurred.After(time.Now()) {
			return ErrConflict
		}
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM asset_association_periods WHERE asset_id=$1 AND event_id=$2 AND customer_id=$3 AND (starts_on IS NULL OR starts_on<=$4) AND (ends_before IS NULL OR ends_before>=$4))`, assetID, event, customer, date).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrConflict
		}
		seen := map[int64]bool{}
		for _, user := range in.ResponsibleUsers {
			if seen[user] {
				return ErrConflict
			}
			seen[user] = true
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE id=$1 AND company_id=$2 AND ($3 OR id=$4 OR EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$5 AND user_id=u.id) OR EXISTS(SELECT 1 FROM asset_service_responsible_users ru JOIN asset_service_revisions r ON r.id=ru.revision_id WHERE r.entry_id=$6 AND ru.user_id=u.id)))`, user, p.CompanyID, p.Allows("directory.view", All), a.UserID, jobID, id).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrConflict
			}
		}
		if id == 0 {
			if err = tx.QueryRow(ctx, `INSERT INTO asset_service_entries(company_id,asset_id,job_id,link_id,customer_id,association_event_id,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, p.CompanyID, assetID, jobID, link, customer, event, a.UserID).Scan(&id); err != nil {
				return err
			}
		}
		revision := uuid.NewString()
		if _, err = tx.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id,predecessor) VALUES($1,$2,$3,$4,$5,nullif($6,'')::uuid)`, revision, id, date, in.Summary, a.UserID, predecessor); err != nil {
			return err
		}
		for _, user := range in.ResponsibleUsers {
			if _, err = tx.Exec(ctx, `INSERT INTO asset_service_responsible_users(revision_id,user_id) VALUES($1,$2)`, revision, user); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, "service_entry."+action, scope, "service_entry", id, map[string]any{"job_id": jobID, "asset_id": assetID, "revision": revision, "content": in, "operation_id": operationID, "client_key": operationClientKey(a), "occurred_at": occurred})
	}()
	return id, err
}

func (s *Service) JobAssetServiceEntries(ctx context.Context, a Actor, jobID, assetID int64) ([]ServiceEntry, error) {
	result := []ServiceEntry{}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		_, _, customer, _, err := serviceEntryRelationship(ctx, tx, p, a, jobID, assetID, "view")
		if err != nil {
			return err
		}
		result, err = serviceEntryHistory(ctx, tx, assetID, customer)
		return err
	})
	return result, err
}

// AssetServiceEntries returns lifetime office history or a single Customer's
// recorded service periods. It never grants access to the contributing Jobs.
func (s *Service) AssetServiceEntries(ctx context.Context, a Actor, assetID, customerID int64) ([]ServiceEntry, error) {
	result := []ServiceEntry{}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("service_entry.view", All) {
			return ErrNotFound
		}
		if _, err := parentAccess(ctx, tx, p, "asset", assetID, "", false); err != nil {
			return err
		}
		if customerID != 0 {
			if _, err := parentAccess(ctx, tx, p, "customer", customerID, "", false); err != nil {
				return err
			}
		}
		var err error
		result, err = serviceEntryHistory(ctx, tx, assetID, customerID)
		return err
	})
	return result, err
}

func serviceEntryHistory(ctx context.Context, q queryer, assetID, customerID int64) ([]ServiceEntry, error) {
	result := []ServiceEntry{}
	rows, err := q.Query(ctx, `SELECT e.id,r.id::text,r.service_date::text,r.summary,j.job_type,r.accepted_at,ARRAY(SELECT u.name FROM asset_service_responsible_users ru JOIN users u ON u.id=ru.user_id WHERE ru.revision_id=r.id ORDER BY u.id),e.job_id,ARRAY(SELECT user_id FROM asset_service_responsible_users WHERE revision_id=r.id ORDER BY user_id) FROM asset_service_entries e JOIN asset_service_revisions r ON r.entry_id=e.id JOIN jobs j ON j.id=e.job_id WHERE e.asset_id=$1 AND ($2::bigint=0 OR e.customer_id=$2) AND NOT EXISTS(SELECT 1 FROM asset_service_revisions next WHERE next.predecessor=r.id) ORDER BY r.service_date,e.id`, assetID, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry ServiceEntry
		if err = rows.Scan(&entry.ID, &entry.Revision, &entry.ServiceDate, &entry.Summary, &entry.JobTitle, &entry.RevisedAt, &entry.ResponsibleNames, &entry.JobID, &entry.ResponsibleUsers); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

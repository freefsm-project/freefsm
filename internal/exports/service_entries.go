package exports

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// ServiceEntry exports office lifetime evidence, never operational Job context.
type ServiceEntry struct{}
type ServiceEntryOptions struct {
	Search   string `json:"search"`
	Sort     string `json:"sort"`
	Archived bool   `json:"archived"`
	AssetID  int64  `json:"asset_id"`
}

func (ServiceEntry) Kind() string               { return "service-entry-v1" }
func (ServiceEntry) Permission() string         { return "service_entry.export" }
func (ServiceEntry) Scope() authorization.Scope { return authorization.All }
func (ServiceEntry) Columns() []string {
	return []string{"service_entry_id", "asset_id", "asset_number", "asset_name", "asset_archived", "job_id", "job_title", "service_date", "summary", "responsible_users", "revision_id", "revised_at", "actor_id", "actor", "accepted_at", "revisions"}
}
func serviceEntryOptions(raw json.RawMessage) (ServiceEntryOptions, error) {
	var o ServiceEntryOptions
	if err := assetDecodeOptions(raw, &o); err != nil {
		return o, err
	}
	if o.AssetID < 0 {
		return o, ErrConflict
	}
	switch o.Sort {
	case "", "date", "date_desc", "asset", "updated":
	default:
		return o, ErrConflict
	}
	return o, nil
}
func (ServiceEntry) Validate(raw json.RawMessage) error {
	_, err := serviceEntryOptions(raw)
	return err
}
func (ServiceEntry) Authorize(p authorization.Principal, raw json.RawMessage) error {
	o, err := serviceEntryOptions(raw)
	if err != nil {
		return err
	}
	if !p.Allows("service_entry.export", authorization.All) || !p.Allows("service_entry.view", authorization.All) || !p.Allows("asset.view", authorization.All) || o.Archived && !p.Allows("asset.archived.view", authorization.All) {
		return authorization.ErrForbidden
	}
	return nil
}
func (d ServiceEntry) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	o, _ := serviceEntryOptions(raw)
	if o.AssetID == 0 {
		return nil
	}
	// Selected archived Assets require explicit inclusion even if the user has
	// archive authority. This also guards empty-result exports.
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, o.AssetID, p.CompanyID, o.Archived).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return authorization.ErrForbidden
	}
	return nil
}

type serviceEntryPersonDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type serviceEntryRevisionDTO struct {
	ID               string                  `json:"id"`
	Predecessor      *string                 `json:"predecessor"`
	ServiceDate      string                  `json:"service_date"`
	Summary          string                  `json:"summary"`
	ResponsibleUsers []serviceEntryPersonDTO `json:"responsible_users"`
	Actor            serviceEntryPersonDTO   `json:"actor"`
	AcceptedAt       time.Time               `json:"accepted_at"`
}
type serviceEntryExportReference struct {
	ID       int64 `json:"id"`
	AssetID  int64 `json:"asset_id"`
	Archived bool  `json:"archived"`
}

func (d ServiceEntry) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.AuthorizeSelection(ctx, tx, p, raw); err != nil {
		return err
	}
	o, _ := serviceEntryOptions(raw)
	order := "r.service_date,e.id"
	switch o.Sort {
	case "date_desc":
		order = "r.service_date DESC,e.id DESC"
	case "asset":
		order = "a.number,a.id,r.service_date,e.id"
	case "updated":
		order = "r.accepted_at DESC,e.id DESC"
	}
	_, err := tx.Exec(ctx, `DECLARE service_entry_export_snapshot NO SCROLL CURSOR FOR SELECT e.id,a.id,a.number,a.name,a.deleted_at IS NOT NULL,e.job_id,j.job_type,r.service_date::text,r.summary,r.id::text,r.accepted_at,e.actor_id,actor.name,e.accepted_at
 FROM asset_service_entries e JOIN assets a ON a.id=e.asset_id AND a.company_id=e.company_id JOIN jobs j ON j.id=e.job_id JOIN users actor ON actor.id=e.actor_id
 JOIN asset_service_revisions r ON r.entry_id=e.id AND NOT EXISTS(SELECT 1 FROM asset_service_revisions next WHERE next.predecessor=r.id)
 WHERE e.company_id=$1 AND ($2 OR a.deleted_at IS NULL) AND ($3::bigint=0 OR a.id=$3) AND ($4='' OR strpos(lower(concat_ws(' ',e.id::text,a.number,a.name,j.job_type,r.summary)),lower($4))>0) ORDER BY `+order, p.CompanyID, o.Archived, o.AssetID, o.Search)
	if err != nil {
		return err
	}
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 128 FROM service_entry_export_snapshot`)
		if err != nil {
			return err
		}
		batch := []Record{}
		for rows.Next() {
			v := make([]string, len(d.Columns()))
			var ref serviceEntryExportReference
			var revised, accepted time.Time
			var job, actor int64
			if err := rows.Scan(&ref.ID, &ref.AssetID, &v[2], &v[3], &ref.Archived, &job, &v[6], &v[7], &v[8], &v[10], &revised, &actor, &v[13], &accepted); err != nil {
				rows.Close()
				return err
			}
			v[0] = fmt.Sprint(ref.ID)
			v[1] = fmt.Sprint(ref.AssetID)
			v[4] = fmt.Sprint(ref.Archived)
			v[5] = fmt.Sprint(job)
			v[11] = revised.Format(time.RFC3339Nano)
			v[12] = fmt.Sprint(actor)
			v[14] = accepted.Format(time.RFC3339Nano)
			b, _ := json.Marshal(ref)
			batch = append(batch, Record{Values: v, Reference: b})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, record := range batch {
			var ref serviceEntryExportReference
			if err := json.Unmarshal(record.Reference, &ref); err != nil {
				return err
			}
			history, err := serviceEntryExportHistory(ctx, tx, ref.ID)
			if err != nil {
				return err
			}
			for _, r := range history {
				if r.ID == record.Values[10] {
					b, err := json.Marshal(r.ResponsibleUsers)
					if err != nil {
						return err
					}
					record.Values[9] = string(b)
				}
			}
			b, err := json.Marshal(history)
			if err != nil {
				return err
			}
			record.Values[15] = string(b)
			if err := emit(record); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `CLOSE service_entry_export_snapshot`)
	return err
}
func serviceEntryExportHistory(ctx context.Context, tx pgx.Tx, id int64) ([]serviceEntryRevisionDTO, error) {
	history := []serviceEntryRevisionDTO{}
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.predecessor::text,r.service_date::text,r.summary,r.actor_id,u.name,r.accepted_at,
 coalesce((SELECT jsonb_agg(jsonb_build_object('id',ru.user_id,'name',person.name) ORDER BY ru.user_id) FROM asset_service_responsible_users ru JOIN users person ON person.id=ru.user_id WHERE ru.revision_id=r.id),'[]'::jsonb)
 FROM asset_service_revisions r JOIN users u ON u.id=r.actor_id WHERE r.entry_id=$1 ORDER BY r.accepted_at,r.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r serviceEntryRevisionDTO
		var people []byte
		if err := rows.Scan(&r.ID, &r.Predecessor, &r.ServiceDate, &r.Summary, &r.Actor.ID, &r.Actor.Name, &r.AcceptedAt, &people); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(people, &r.ResponsibleUsers); err != nil {
			return nil, err
		}
		history = append(history, r)
	}
	return history, rows.Err()
}
func (ServiceEntry) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref serviceEntryExportReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	if !p.Allows("service_entry.export", authorization.All) || !p.Allows("service_entry.view", authorization.All) {
		return authorization.ErrForbidden
	}
	if err := recheckExportAsset(ctx, tx, p, ref.AssetID, ref.Archived); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM asset_service_entries WHERE id=$1 AND asset_id=$2 AND company_id=$3)`, ref.ID, ref.AssetID, p.CompanyID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return authorization.ErrForbidden
	}
	return nil
}

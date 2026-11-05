package authorization

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResolveServiceEntryReference maps the existing sync identity namespace to its
// installed identity. It confers no authority: the domain transaction validates
// the exact Job, Asset, relationship and current permissions afterwards.
func (s *Service) ResolveServiceEntryReference(ctx context.Context, kind, reference string) (int64, error) {
	if kind != "asset" && kind != "service_entries" {
		return 0, ErrNotFound
	}
	if id, err := strconv.ParseInt(reference, 10, 64); err == nil && id > 0 {
		return id, nil
	}
	id, err := uuid.Parse(reference)
	if err != nil {
		return 0, ErrNotFound
	}
	var installed int64
	err = s.Pool.QueryRow(ctx, `SELECT installed_id FROM job_sync_identities WHERE resource_type=$1 AND opaque_id=$2`, kind, id.String()).Scan(&installed)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return installed, err
}

// Enrich only the already-authorized bounded Asset projection. Office lifetime
// history and the direct context DTO keep their established disclosure contracts.
func syncServiceEntryContext(ctx context.Context, tx pgx.Tx, p Principal, a Actor, jobID int64, masters JobMasterContext, data map[string]any) error {
	if !p.Allows("service_entry.view", Assigned) {
		return nil
	}
	assets, _ := data["assets"].([]any)
	for i, asset := range masters.Assets {
		if i >= len(assets) {
			return errors.New("incomplete synchronized Asset projection")
		}
		projected := assets[i].(map[string]any)
		_, link, _, _, err := serviceEntryRelationship(ctx, tx, p, a, jobID, asset.ID, "view")
		if err != nil {
			return err
		}
		participants := []map[string]any{}
		rows, err := tx.Query(ctx, `SELECT id,name FROM users u WHERE company_id=$1 AND (id=$2 OR EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$3 AND user_id=u.id) OR $4) ORDER BY name,id`, p.CompanyID, a.UserID, jobID, p.Allows("directory.view", All))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var name string
			if err = rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			participants = append(participants, map[string]any{"id": id, "name": name})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		projected["service_entry_context"] = map[string]any{"link_id": link, "can_create": p.Allows("service_entry.create", Assigned), "participants": participants}
		entries, _ := projected["service_entries"].([]any)
		for index, entry := range asset.ServiceEntries {
			row := entries[index].(map[string]any)
			row["asset_id"] = projected["id"]
			row["job_id"], err = syncIdentity(ctx, tx, "job", entry.JobID)
			if err != nil {
				return err
			}
			canRevise := p.Allows("service_entry.revise", Assigned) && entry.JobID == jobID && entry.LinkID == link
			row["can_revise"] = canRevise
			if canRevise {
				row["link_id"] = link
				row["responsible_user_ids"] = entry.ResponsibleUsers
			}
		}
	}
	// Preserve the existing singleton alias with exactly the same capabilities.
	if len(assets) > 0 {
		data["asset"] = assets[0]
	}
	return nil
}

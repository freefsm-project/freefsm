package authorization

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

type JobLinkChoice struct {
	ID   int64
	Name string
}
type JobLinks struct {
	ProjectID, LocationID, AssetID int64
	Projects, Locations, Assets    []JobLinkChoice
}

func (s *Service) JobLinks(ctx context.Context, a Actor, id int64) (JobLinks, error) {
	var out JobLinks
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "job.relations.edit", true); err != nil {
			return err
		}
		var customer int64
		if err := tx.QueryRow(ctx, `SELECT customer_id,coalesce(project_id,0),coalesce(location_id,0),coalesce(asset_id,0) FROM jobs WHERE id=$1`, id).Scan(&customer, &out.ProjectID, &out.LocationID, &out.AssetID); err != nil {
			return err
		}
		for _, entry := range []struct {
			kind, table, label, owner string
			current                   int64
			dest                      *[]JobLinkChoice
		}{
			{"project", "projects", "name", "customer_id", out.ProjectID, &out.Projects},
			{"location", "locations", "title", "object_id", out.LocationID, &out.Locations},
			{"asset", "assets", "name", "customer_id", out.AssetID, &out.Assets},
		} {
			if !p.Allows(entry.kind+".view", All) {
				if entry.current > 0 {
					*entry.dest = append(*entry.dest, JobLinkChoice{entry.current, "Retain current link"})
				}
				continue
			}
			extra := ""
			if entry.kind == "location" {
				extra = " AND object_type='customer'"
			}
			rows, err := tx.Query(ctx, `SELECT id,`+entry.label+`||CASE WHEN deleted_at IS NOT NULL THEN ' (archived)' ELSE '' END FROM `+entry.table+` WHERE company_id=$1 AND `+entry.owner+`=$2`+extra+` AND (deleted_at IS NULL OR id=$3) ORDER BY id`, p.CompanyID, customer, entry.current)
			if err != nil {
				return err
			}
			for rows.Next() {
				var c JobLinkChoice
				if err := rows.Scan(&c.ID, &c.Name); err != nil {
					rows.Close()
					return err
				}
				*entry.dest = append(*entry.dest, c)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) SetJobLinks(ctx context.Context, a Actor, id int64, in JobLinks) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "job.relations.edit", true); err != nil {
			return err
		}
		if err := saveaction.RequireRevision(ctx, tx, p.UserID, fmt.Sprintf("/jobs/%d", id)); err != nil {
			return err
		}
		var customer, contact int64
		var before JobLinks
		if err := tx.QueryRow(ctx, `SELECT customer_id,coalesce(customer_contact_id,0),coalesce(project_id,0),coalesce(location_id,0),coalesce(asset_id,0) FROM jobs WHERE id=$1`, id).Scan(&customer, &contact, &before.ProjectID, &before.LocationID, &before.AssetID); err != nil {
			return err
		}
		if err := s.JobInputs(ctx, tx, a, id, customer, false, map[string]int64{"contact": contact, "project": in.ProjectID, "location": in.LocationID, "asset": in.AssetID}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET project_id=nullif($2,0),location_id=nullif($3,0),asset_id=nullif($4,0),updated_at=clock_timestamp() WHERE id=$1`, id, in.ProjectID, in.LocationID, in.AssetID); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "job.relations.edit", All, "job", id, map[string]any{"links_before": before, "links_after": in})
	})
}

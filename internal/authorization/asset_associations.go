package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AssetAssociationInput struct {
	Action, EffectiveDate, Reason             string
	CustomerID, LocationID, EventID, Revision int64
}

// ChangeAssetAssociation owns both the factual timeline and its current projection.
// Authority writers, Job linking, and master lifecycle writers share the fence.
func (s *Service) ChangeAssetAssociation(ctx context.Context, a Actor, id int64, in AssetAssociationInput) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		permission := "asset." + in.Action
		if in.Action == "correct" {
			permission = "asset.history.correct"
		}
		if in.Action != "transfer" && in.Action != "place" && in.Action != "return" && in.Action != "correct" {
			return ErrConflict
		}
		if _, err := parentAccess(ctx, tx, p, "asset", id, permission, true); err != nil {
			return err
		}
		var class string
		var revision, previousEvent int64
		var before json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT ownership_class,jsonb_build_object('asset',to_jsonb(a),'timeline',(SELECT jsonb_agg(to_jsonb(p) ORDER BY starts_on NULLS FIRST,event_id) FROM asset_association_periods p WHERE p.asset_id=a.id)),(SELECT count(*) FROM asset_association_events WHERE asset_id=a.id)+(SELECT count(*) FROM asset_association_origin_revisions WHERE asset_id=a.id),coalesce((SELECT event_id FROM asset_association_facts WHERE asset_id=a.id ORDER BY effective_date DESC,event_id DESC LIMIT 1),0) FROM assets a WHERE id=$1 FOR UPDATE`, id).Scan(&class, &before, &revision, &previousEvent); err != nil {
			return err
		}
		if revision != in.Revision {
			return ErrConflict
		}
		date, err := time.Parse("2006-01-02", in.EffectiveDate)
		if err != nil || strings.TrimSpace(in.Reason) == "" {
			return fmt.Errorf("%w: effective date and reason are required", ErrConflict)
		}
		var today time.Time
		if err = tx.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$1),'UTC'))::date`, p.CompanyID).Scan(&today); err != nil {
			return err
		}
		if date.After(today) {
			return fmt.Errorf("%w: future association dates are not allowed", ErrConflict)
		}
		if in.Action == "return" {
			if class != "company_owned" || in.CustomerID != 0 || in.LocationID != 0 {
				return ErrConflict
			}
		}
		if in.Action == "place" && (class != "company_owned" || in.CustomerID == 0) {
			return ErrConflict
		}
		if in.Action == "transfer" && in.CustomerID == 0 {
			return ErrConflict
		}
		if class == "customer_owned" && in.CustomerID == 0 || in.CustomerID == 0 && in.LocationID != 0 {
			return ErrConflict
		}
		if in.CustomerID > 0 {
			if !p.Allows("customer.view", All) {
				return ErrNotFound
			}
			active, err := customerLifecycleState(ctx, tx, p, in.CustomerID, true)
			if err != nil {
				return err
			}
			if !active {
				return ErrConflict
			}
		}
		if in.LocationID > 0 {
			if !p.Allows("location.view", All) {
				return ErrNotFound
			}
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM locations WHERE id=$1 AND company_id=$2 AND object_type='customer' AND object_id=$3 AND deleted_at IS NULL)`, in.LocationID, p.CompanyID, in.CustomerID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrConflict
			}
		}
		var event any
		originCorrection := in.Action == "correct" && in.EventID == 0
		if in.Action == "correct" {
			query := `SELECT to_jsonb(f) FROM asset_association_facts f WHERE asset_id=$1 AND event_id=$2`
			if originCorrection {
				query = `SELECT to_jsonb(f) FROM asset_association_origin_facts f WHERE asset_id=$1 AND $2::bigint=0`
			}
			if err := tx.QueryRow(ctx, query, id, in.EventID).Scan(&before); err != nil {
				if err == pgx.ErrNoRows {
					return ErrNotFound
				}
				return err
			}
			event = in.EventID
		} else if in.EventID != 0 {
			return ErrConflict
		}
		var affectedEvent int64
		if originCorrection {
			_, err = tx.Exec(ctx, `INSERT INTO asset_association_origin_revisions(asset_id,effective_date,customer_id,location_id,reason,actor_id,before_fact) VALUES($1,$2,nullif($3,0),nullif($4,0),$5,$6,$7)`, id, date, in.CustomerID, in.LocationID, strings.TrimSpace(in.Reason), a.UserID, before)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO asset_association_events(asset_id,event_id,action,effective_date,customer_id,location_id,reason,actor_id,before_fact) VALUES($1,$2,$3,$4,nullif($5,0),nullif($6,0),$7,$8,$9) RETURNING coalesce(event_id,id)`, id, event, in.Action, date, in.CustomerID, in.LocationID, strings.TrimSpace(in.Reason), a.UserID, before).Scan(&affectedEvent)
		}
		if err != nil {
			return err
		}
		// Validate every derived transition and retained Job association, not just
		// today's owner. Explicit unlink is a separate authorized repair.
		var invalid bool
		if err = tx.QueryRow(ctx, `WITH ordered AS (
    SELECT p.*,lag(customer_id) OVER(ORDER BY starts_on NULLS FIRST,event_id) previous_customer,
    lag(starts_on) OVER(ORDER BY starts_on NULLS FIRST,event_id) previous_date
    FROM asset_association_periods p WHERE asset_id=$1)
   SELECT EXISTS(SELECT 1 FROM ordered f JOIN asset_association_events e ON e.id=f.event_id WHERE
    f.previous_date>f.starts_on OR
    (e.action='place' AND (f.previous_customer IS NOT NULL OR f.customer_id IS NULL)) OR
    (e.action='return' AND (f.previous_customer IS NULL OR f.customer_id IS NOT NULL)) OR
    (e.action='transfer' AND (f.previous_customer IS NULL OR f.customer_id IS NULL OR f.previous_customer=f.customer_id)))
   OR EXISTS(SELECT 1 FROM job_asset_links l WHERE l.asset_id=$1 AND l.unlinked_at IS NULL AND (
    (l.linked_on IS NULL AND ((NOT $4 AND $2::date<$3::date) OR NOT EXISTS(SELECT 1 FROM asset_association_periods p WHERE p.asset_id=l.asset_id AND p.event_id=l.association_event_id AND p.customer_id=l.customer_id))) OR
    (l.linked_on IS NOT NULL AND NOT EXISTS(SELECT 1 FROM asset_association_periods p WHERE p.asset_id=l.asset_id AND p.event_id=l.association_event_id AND p.customer_id=l.customer_id AND (p.starts_on IS NULL OR p.starts_on<=l.linked_on) AND (p.ends_before IS NULL OR l.linked_on<=p.ends_before)))))`, id, date, today, originCorrection).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return fmt.Errorf("%w: repair affected Job relationships or overlapping association dates first", ErrConflict)
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.asset_association','allowed',true)`); err != nil {
			return err
		}
		// Location is also editable within the current Customer. Revising a past
		// period must not reset that unrelated current detail to an older snapshot.
		if _, err = tx.Exec(ctx, `UPDATE assets SET customer_id=f.customer_id,location_id=CASE WHEN f.event_id<>$2 OR f.event_id=$3 THEN f.location_id ELSE assets.location_id END,updated_at=clock_timestamp() FROM (SELECT event_id,customer_id,location_id FROM asset_association_periods WHERE asset_id=$1 ORDER BY starts_on DESC NULLS LAST,event_id DESC LIMIT 1) f WHERE id=$1`, id, previousEvent, affectedEvent); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE job_asset_links l SET operational=false FROM assets a WHERE a.id=$1 AND l.asset_id=a.id AND l.operational AND l.customer_id IS DISTINCT FROM a.customer_id`, id); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, permission, All, "asset", id, map[string]any{"before": before, "after": in})
	})
}

type AssetAssociationHistory struct {
	Revision      int64
	Number, Class string
	Periods       []AssetAssociationPeriod
	Events        []AssetAssociationEvidence
}
type AssetAssociationPeriod struct {
	EventID, CustomerID, LocationID          int64
	Customer, Location, StartsOn, EndsBefore string
}

type CustomerAssetPeriod struct {
	AssetID                      int64
	Number, StartsOn, EndsBefore string
}

func (s *Service) CustomerAssetHistory(ctx context.Context, a Actor, id int64) ([]CustomerAssetPeriod, error) {
	var out []CustomerAssetPeriod
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("customer.view", All) || !p.Allows("asset.view", All) {
			return ErrNotFound
		}
		if _, err := parentAccess(ctx, tx, p, "customer", id, "", false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT p.asset_id,a.number,coalesce(p.starts_on::text,'Unknown — observed association'),coalesce(p.ends_before::text,'Current') FROM asset_association_periods p JOIN assets a ON a.id=p.asset_id WHERE p.customer_id=$1 AND a.company_id=$2 AND (a.deleted_at IS NULL OR $3) ORDER BY a.number,p.starts_on NULLS FIRST,p.event_id`, id, p.CompanyID, p.Allows("asset.archived.view", All))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v CustomerAssetPeriod
			if err = rows.Scan(&v.AssetID, &v.Number, &v.StartsOn, &v.EndsBefore); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

type AssetAssociationEvidence struct {
	ID, EventID                               int64
	Action, EffectiveDate, Reason, RecordedAt string
}

func (s *Service) AssetAssociationHistory(ctx context.Context, a Actor, id int64) (AssetAssociationHistory, error) {
	var out AssetAssociationHistory
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("asset.view", All) {
			return ErrNotFound
		}
		if _, err := parentAccess(ctx, tx, p, "asset", id, "", false); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT number,ownership_class FROM assets WHERE id=$1`, id).Scan(&out.Number, &out.Class); err != nil {
			return err
		}
		// Related master grants are independent. Archived references are bounded
		// labels with badges (ARC-04), never detail/navigation authority.
		rows, err := tx.Query(ctx, `SELECT p.event_id,
 CASE WHEN $2 THEN coalesce(p.customer_id,0) ELSE 0 END,
 CASE WHEN $3 THEN coalesce(p.location_id,0) ELSE 0 END,
 CASE WHEN p.customer_id IS NULL THEN 'Unplaced' WHEN NOT $2 THEN 'Restricted Customer' ELSE c.display_name||CASE WHEN c.deleted_at IS NOT NULL THEN ' (Archived)' ELSE '' END END,
 CASE WHEN p.location_id IS NULL THEN '' WHEN NOT $3 THEN 'Restricted Location' ELSE l.title||CASE WHEN l.deleted_at IS NOT NULL THEN ' (Archived)' ELSE '' END END,
 coalesce(p.starts_on::text,'Unknown — observed association'),coalesce(p.ends_before::text,'Current')
 FROM asset_association_periods p LEFT JOIN customers c ON c.id=p.customer_id LEFT JOIN locations l ON l.id=p.location_id WHERE p.asset_id=$1 ORDER BY p.starts_on NULLS FIRST,p.event_id`, id, p.Allows("customer.view", All), p.Allows("location.view", All))
		if err != nil {
			return err
		}
		for rows.Next() {
			var v AssetAssociationPeriod
			if err = rows.Scan(&v.EventID, &v.CustomerID, &v.LocationID, &v.Customer, &v.Location, &v.StartsOn, &v.EndsBefore); err != nil {
				rows.Close()
				return err
			}
			out.Periods = append(out.Periods, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id,coalesce(event_id,id),action,effective_date::text,reason,recorded_at::text FROM asset_association_events WHERE asset_id=$1
 UNION ALL SELECT id,0,'correct',effective_date::text,reason,recorded_at::text FROM asset_association_origin_revisions WHERE asset_id=$1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v AssetAssociationEvidence
			if err = rows.Scan(&v.ID, &v.EventID, &v.Action, &v.EffectiveDate, &v.Reason, &v.RecordedAt); err != nil {
				return err
			}
			out.Events = append(out.Events, v)
			out.Revision++
		}
		return rows.Err()
	})
	return out, err
}

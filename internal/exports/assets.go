package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

type Asset struct{}

type AssetOptions struct {
	Search        string `json:"search"`
	Sort          string `json:"sort"`
	Archived      bool   `json:"archived"`
	ArchivedOnly  bool   `json:"archived_only"`
	CustomerID    int64  `json:"customer_id"`
	AssetTypeID   int64  `json:"asset_type_id"`
	AssetStatusID int64  `json:"asset_status_id"`
}

func (Asset) Kind() string               { return "asset-operational-v1" }
func (Asset) Permission() string         { return "asset.export" }
func (Asset) Scope() authorization.Scope { return authorization.All }
func (Asset) Columns() []string {
	return []string{"asset_id", "number", "name", "ownership_class", "association_kind", "customer_id", "customer", "location_id", "location", "type_id", "type", "status_id", "status", "serial_number", "manufacturer", "model", "installed_at", "warranty_expires", "service_notes", "custom_fields", "archived", "association_periods", "association_events"}
}
func assetOptions(raw json.RawMessage) (AssetOptions, error) {
	var o AssetOptions
	if err := assetDecodeOptions(raw, &o); err != nil {
		return o, err
	}
	if o.CustomerID < 0 || o.AssetTypeID < 0 || o.AssetStatusID < 0 {
		return o, ErrConflict
	}
	if o.ArchivedOnly && !o.Archived {
		return o, ErrConflict
	}
	switch o.Sort {
	case "", "number", "number_desc", "name", "name_desc", "updated":
	default:
		return o, ErrConflict
	}
	return o, nil
}
func assetDecodeOptions(raw json.RawMessage, out any) error {
	if !json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrConflict
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrConflict
	}
	return nil
}
func (Asset) Validate(raw json.RawMessage) error { _, err := assetOptions(raw); return err }
func (Asset) Authorize(p authorization.Principal, raw json.RawMessage) error {
	o, err := assetOptions(raw)
	if err != nil {
		return err
	}
	if !p.Allows("asset.export", authorization.All) || !p.Allows("asset.view", authorization.All) || o.Archived && !p.Allows("asset.archived.view", authorization.All) {
		return authorization.ErrForbidden
	}
	if o.CustomerID != 0 && !p.Allows("customer.view", authorization.All) {
		return authorization.ErrForbidden
	}
	return nil
}

// A selected Customer is authority-bearing even when no Assets match. The
// durable engine repeats this check at acceptance, generation and delivery.
func (d Asset) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	o, _ := assetOptions(raw)
	var visible bool
	if o.CustomerID != 0 {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, o.CustomerID, p.CompanyID, o.Archived && p.Allows("customer.archived.view", authorization.All)).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return authorization.ErrForbidden
		}
	}
	for _, selection := range []struct {
		id    int64
		table string
	}{{o.AssetTypeID, "asset_types"}, {o.AssetStatusID, "asset_statuses"}} {
		if selection.id == 0 {
			continue
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+selection.table+` WHERE id=$1 AND (company_id=$2 OR company_id IS NULL))`, selection.id, p.CompanyID).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return authorization.ErrForbidden
		}
	}
	return nil
}

// References record optional authority only when a value was actually disclosed.
// Related archived labels are bounded references, not archived detail access.
type assetExportReference struct {
	ID       int64 `json:"id"`
	Archived bool  `json:"archived"`
	Customer bool  `json:"customer"`
	Location bool  `json:"location"`
}
type assetAssociationPeriodDTO struct {
	EventID    int64   `json:"event_id"`
	CustomerID *int64  `json:"customer_id"`
	Customer   string  `json:"customer"`
	LocationID *int64  `json:"location_id"`
	Location   string  `json:"location"`
	StartsOn   *string `json:"starts_on"`
	EndsBefore *string `json:"ends_before"`
}
type assetAssociationEventDTO struct {
	ID            int64     `json:"id"`
	EventID       int64     `json:"event_id"`
	Action        string    `json:"action"`
	EffectiveDate string    `json:"effective_date"`
	Reason        string    `json:"reason"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func (d Asset) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.AuthorizeSelection(ctx, tx, p, raw); err != nil {
		return err
	}
	o, _ := assetOptions(raw)
	order := "lower(a.name),a.id"
	switch o.Sort {
	case "number":
		order = "a.number,a.id"
	case "number_desc":
		order = "a.number DESC,a.id DESC"
	case "name":
		order = "lower(a.name),a.id"
	case "name_desc":
		order = "lower(a.name) DESC,a.id DESC"
	case "updated":
		order = "a.updated_at DESC,a.id DESC"
	}
	// Match the collection's Ent ContainsFold predicates: literal substring
	// matching within each field, using PostgreSQL ILIKE and escaped wildcards.
	search := "%" + strings.ToLower(strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(o.Search)) + "%"
	_, err := tx.Exec(ctx, `DECLARE asset_export_snapshot NO SCROLL CURSOR FOR SELECT a.id,a.number,a.name,a.ownership_class,
 CASE WHEN a.ownership_class='customer_owned' THEN 'owner' ELSE 'placement' END,
 CASE WHEN $4 THEN coalesce(a.customer_id::text,'') ELSE '' END,
 CASE WHEN a.customer_id IS NULL THEN 'Unplaced' WHEN NOT $4 THEN 'Restricted Customer' ELSE c.display_name||CASE WHEN c.deleted_at IS NOT NULL THEN ' (Archived)' ELSE '' END END,
 CASE WHEN $5 THEN coalesce(a.location_id::text,'') ELSE '' END,
 CASE WHEN a.location_id IS NULL THEN '' WHEN NOT $5 THEN 'Restricted Location' ELSE l.title||CASE WHEN l.deleted_at IS NOT NULL THEN ' (Archived)' ELSE '' END END,
 a.asset_type_id::text,t.name,coalesce(a.asset_status_id::text,''),coalesce(s.name,''),a.serial_number,a.manufacturer,a.model,a.installed_at,a.warranty_expires,a.notes,a.custom_fields::text,a.deleted_at IS NOT NULL
 FROM assets a JOIN asset_types t ON t.id=a.asset_type_id LEFT JOIN asset_statuses s ON s.id=a.asset_status_id LEFT JOIN customers c ON c.id=a.customer_id LEFT JOIN locations l ON l.id=a.location_id
 WHERE a.company_id=$1 AND ($2 OR a.deleted_at IS NULL)
 AND (a.number ILIKE $3 OR a.name ILIKE $3 OR a.serial_number ILIKE $3 OR a.model ILIKE $3 OR a.manufacturer ILIKE $3)
 AND ($6::bigint=0 OR a.customer_id=$6) AND ($7::bigint=0 OR a.asset_type_id=$7) AND ($8::bigint=0 OR a.asset_status_id=$8)
 AND (NOT $9 OR a.deleted_at IS NOT NULL)
 ORDER BY `+order, p.CompanyID, o.Archived, search, p.Allows("customer.view", authorization.All), p.Allows("location.view", authorization.All), o.CustomerID, o.AssetTypeID, o.AssetStatusID, o.ArchivedOnly)
	if err != nil {
		return err
	}
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 128 FROM asset_export_snapshot`)
		if err != nil {
			return err
		}
		batch := []Record{}
		for rows.Next() {
			v := make([]string, len(d.Columns()))
			var ref assetExportReference
			var installed, warranty *time.Time
			if err := rows.Scan(&ref.ID, &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &v[9], &v[10], &v[11], &v[12], &v[13], &v[14], &v[15], &installed, &warranty, &v[18], &v[19], &ref.Archived); err != nil {
				rows.Close()
				return err
			}
			v[0] = fmt.Sprint(ref.ID)
			v[16] = exportInstant(installed)
			v[17] = exportInstant(warranty)
			v[20] = fmt.Sprint(ref.Archived)
			ref.Customer = v[5] != ""
			ref.Location = v[7] != ""
			encoded, _ := json.Marshal(ref)
			batch = append(batch, Record{Values: v, Reference: encoded})
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
			var ref assetExportReference
			if err := json.Unmarshal(record.Reference, &ref); err != nil {
				return err
			}
			periods, events, err := assetExportHistory(ctx, tx, p, &ref)
			if err != nil {
				return err
			}
			b, err := json.Marshal(periods)
			if err != nil {
				return err
			}
			record.Values[21] = string(b)
			b, err = json.Marshal(events)
			if err != nil {
				return err
			}
			record.Values[22] = string(b)
			record.Reference, err = json.Marshal(ref)
			if err != nil {
				return err
			}
			if err := emit(record); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `CLOSE asset_export_snapshot`)
	return err
}

func assetExportHistory(ctx context.Context, tx pgx.Tx, p authorization.Principal, ref *assetExportReference) ([]assetAssociationPeriodDTO, []assetAssociationEventDTO, error) {
	periods := []assetAssociationPeriodDTO{}
	events := []assetAssociationEventDTO{}
	rows, err := tx.Query(ctx, `SELECT p.event_id,CASE WHEN $2 THEN p.customer_id END,
 CASE WHEN p.customer_id IS NULL THEN 'Unplaced' WHEN NOT $2 THEN 'Restricted Customer' ELSE c.display_name||CASE WHEN c.deleted_at IS NOT NULL THEN ' (Archived)' ELSE '' END END,
 CASE WHEN $3 THEN p.location_id END,
 CASE WHEN p.location_id IS NULL THEN '' WHEN NOT $3 THEN 'Restricted Location' ELSE l.title||CASE WHEN l.deleted_at IS NOT NULL THEN ' (Archived)' ELSE '' END END,p.starts_on::text,p.ends_before::text
 FROM asset_association_periods p LEFT JOIN customers c ON c.id=p.customer_id LEFT JOIN locations l ON l.id=p.location_id WHERE p.asset_id=$1 ORDER BY p.starts_on NULLS FIRST,p.event_id`, ref.ID, p.Allows("customer.view", authorization.All), p.Allows("location.view", authorization.All))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var v assetAssociationPeriodDTO
		if err := rows.Scan(&v.EventID, &v.CustomerID, &v.Customer, &v.LocationID, &v.Location, &v.StartsOn, &v.EndsBefore); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ref.Customer = ref.Customer || v.CustomerID != nil
		ref.Location = ref.Location || v.LocationID != nil
		periods = append(periods, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	// The allowlist deliberately excludes raw before_fact/evidence, which contain
	// independently restricted master references. Origin corrections retain event 0.
	rows, err = tx.Query(ctx, `SELECT id,coalesce(event_id,id),action,effective_date::text,reason,recorded_at FROM asset_association_events WHERE asset_id=$1 UNION ALL SELECT id,0,'correct',effective_date::text,reason,recorded_at FROM asset_association_origin_revisions WHERE asset_id=$1 ORDER BY id`, ref.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v assetAssociationEventDTO
		if err := rows.Scan(&v.ID, &v.EventID, &v.Action, &v.EffectiveDate, &v.Reason, &v.RecordedAt); err != nil {
			return nil, nil, err
		}
		events = append(events, v)
	}
	return periods, events, rows.Err()
}
func (Asset) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref assetExportReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	if !p.Allows("asset.export", authorization.All) || ref.Customer && !p.Allows("customer.view", authorization.All) || ref.Location && !p.Allows("location.view", authorization.All) {
		return authorization.ErrForbidden
	}
	return recheckExportAsset(ctx, tx, p, ref.ID, ref.Archived)
}
func recheckExportAsset(ctx context.Context, tx pgx.Tx, p authorization.Principal, id int64, archived bool) error {
	if !p.Allows("asset.view", authorization.All) || archived && !p.Allows("asset.archived.view", authorization.All) {
		return authorization.ErrForbidden
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, id, p.CompanyID, p.Allows("asset.archived.view", authorization.All)).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return authorization.ErrForbidden
	}
	return nil
}

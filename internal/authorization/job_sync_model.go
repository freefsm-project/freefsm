package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// syncEntry separates private installed identity/authority anchors from the
// closed public representation. Only Data is ever returned to the client.
type syncEntry struct {
	InstalledID int64          `json:"installed_id"`
	Anchor      string         `json:"anchor"`
	ID          string         `json:"id"`
	Data        map[string]any `json:"data"`
}

func syncIdentity(ctx context.Context, tx pgx.Tx, kind string, id int64) (string, error) {
	var out string
	err := tx.QueryRow(ctx, `INSERT INTO job_sync_identities(resource_type,installed_id,opaque_id) VALUES($1,$2,$3) ON CONFLICT(resource_type,installed_id) DO UPDATE SET installed_id=EXCLUDED.installed_id RETURNING opaque_id`, kind, id, uuid.NewString()).Scan(&out)
	return out, err
}

// Read the existing closed projections, never serializing domain entities.
// Broad Job View permits unassigned Job rows, not unassigned C context.
func syncReadModel(ctx context.Context, tx pgx.Tx, p Principal, a Actor, scope string) ([]syncEntry, error) {
	rows, err := tx.Query(ctx, `SELECT j.id,jsonb_build_object(
 'title',j.job_type,'description',j.subtitle,'field_notes',j.tech_notes,
 'status',jsonb_build_object('name',coalesce(s.name,''),'category',coalesce(s.category_key,'')),
 'start_time',j.start_time,'end_time',j.end_time,'due_date',j.due_date::text,
 'arrival_window_start',j.arrival_window_start,'arrival_window_end',j.arrival_window_end,
 'timezone',coalesce(cs.timezone,'UTC')),
 jsonb_build_array(j.customer_id,j.project_id,j.location_id,j.customer_contact_id,
 EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2),
 (SELECT deleted_at IS NOT NULL FROM projects WHERE id=j.project_id))::text
 FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id LEFT JOIN company_settings cs ON cs.company_id=j.company_id
 WHERE j.company_id=$1 AND j.deleted_at IS NULL AND ($3 OR EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2)) ORDER BY j.id`, p.CompanyID, a.UserID, scope == "all")
	if err != nil {
		return nil, err
	}
	out := []syncEntry{}
	for rows.Next() {
		var e syncEntry
		var raw []byte
		if err = rows.Scan(&e.InstalledID, &raw, &e.Anchor); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &e.Data); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		e := &out[i]
		e.ID, err = syncIdentity(ctx, tx, "job", e.InstalledID)
		if err != nil {
			return nil, err
		}
		e.Data["id"] = e.ID
		fields, err := jobFieldSnapshot(ctx, tx, p, e.InstalledID)
		if err != nil {
			return nil, err
		}
		// Use the same canonical JSON object representation as durable baselines.
		// Struct field order must not manufacture changes after snapshot reload.
		rawFields, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		var fieldData map[string]any
		decoder := json.NewDecoder(bytes.NewReader(rawFields))
		decoder.UseNumber()
		if err := decoder.Decode(&fieldData); err != nil {
			return nil, err
		}
		e.Data["custom_fields"] = fieldData
		e.Data["masters"] = map[string]any{}
		customer, cerr := jobCustomer(ctx, tx, p, a, e.InstalledID)
		if cerr != nil && !errors.Is(cerr, ErrNotFound) {
			return nil, cerr
		}
		if cerr == nil {
			v, err := syncDTO(ctx, tx, "customer", customer)
			if err != nil {
				return nil, err
			}
			e.Data["customer"] = v
		}
		masters, merr := jobMasters(ctx, tx, p, a, e.InstalledID)
		if merr != nil && !errors.Is(merr, ErrNotFound) {
			return nil, merr
		}
		if merr == nil {
			v, err := syncDTO(ctx, tx, "masters", masters)
			if err != nil {
				return nil, err
			}
			e.Data["masters"] = v
			// Membership, lifecycle, and linked-location changes invalidate old context.
			// Field edits do not: they appear in the following state delta.
			anchor := []any{e.Anchor}
			if cerr == nil {
				anchor = append(anchor, customer.ID)
			}
			for _, c := range masters.Contacts {
				anchor = append(anchor, fmt.Sprintf("contact:%d", c.ID))
			}
			if masters.Contact != nil {
				anchor = append(anchor, fmt.Sprintf("contact:%d", masters.Contact.ID))
			}
			if masters.Location != nil {
				anchor = append(anchor, fmt.Sprintf("location:%d", masters.Location.ID))
			}
			for _, asset := range masters.Assets {
				anchor = append(anchor, fmt.Sprintf("asset:%d", asset.ID))
				if asset.Location != nil {
					anchor = append(anchor, fmt.Sprintf("asset-location:%d", asset.Location.ID))
				}
			}
			if masters.Project != nil {
				anchor = append(anchor, "project-location:"+masters.Project.Location)
			}
			b, err := json.Marshal(anchor)
			if err != nil {
				return nil, err
			}
			e.Anchor = string(b)
		}
	}
	return out, nil
}

func syncDTO(ctx context.Context, tx pgx.Tx, kind string, value any) (map[string]any, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err = decoder.Decode(&v); err != nil {
		return nil, err
	}
	if err = syncOpaqueFields(ctx, tx, kind, v); err != nil {
		return nil, err
	}
	return v, nil
}

func syncOpaqueFields(ctx context.Context, tx pgx.Tx, kind string, v map[string]any) error {
	if number, ok := v["id"].(json.Number); ok {
		id, err := number.Int64()
		if err != nil {
			return err
		}
		opaque, err := syncIdentity(ctx, tx, kind, id)
		if err != nil {
			return err
		}
		v["id"] = opaque
	}
	for key, value := range v {
		childKind := key
		switch key {
		case "contacts", "contact":
			childKind = "contact"
		case "assets", "asset":
			childKind = "asset"
		case "location":
			childKind = "location"
		}
		switch nested := value.(type) {
		case map[string]any:
			if err := syncOpaqueFields(ctx, tx, childKind, nested); err != nil {
				return err
			}
		case []any:
			for _, item := range nested {
				if child, ok := item.(map[string]any); ok {
					if err := syncOpaqueFields(ctx, tx, childKind, child); err != nil {
						return err
					}
				}
			}
		}
		if key == "installed_at" || key == "warranty_expires" {
			if raw, ok := value.(string); ok {
				instant, err := time.Parse(time.RFC3339Nano, raw)
				if err != nil {
					return err
				}
				v[key] = instant.Format("2006-01-02")
			}
		}
	}
	return nil
}

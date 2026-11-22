package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// MasterRecordOptions selects all matching rows, never a UI page. Archived is
// an explicit include-archived choice, requiring the resource's archived View.
type MasterRecordOptions struct {
	Search     string `json:"search"`
	Sort       string `json:"sort"`
	Archived   bool   `json:"archived"`
	CustomerID int64  `json:"customer_id,omitempty"`
	StatusID   int64  `json:"status_id,omitempty"`
}

type Contact struct{}
type Location struct{}
type Project struct{}

func (Contact) Kind() string                { return "contact-operational-v1" }
func (Location) Kind() string               { return "location-operational-v1" }
func (Project) Kind() string                { return "project-operational-v1" }
func (Contact) Permission() string          { return "contact.export" }
func (Location) Permission() string         { return "location.export" }
func (Project) Permission() string          { return "project.export" }
func (Contact) Scope() authorization.Scope  { return authorization.All }
func (Location) Scope() authorization.Scope { return authorization.All }
func (Project) Scope() authorization.Scope  { return authorization.All }
func (Contact) Columns() []string {
	return []string{"contact_id", "first_name", "last_name", "role_title", "email", "phone", "service_notes", "notes", "primary_customer_default", "customer_id", "customer_label", "customer_visibility", "job_links_json", "job_links_visibility", "archived"}
}
func (Location) Columns() []string {
	return []string{"location_id", "title", "address_1", "address_2", "city", "state", "zip_code", "latitude", "longitude", "access_notes", "service_notes", "notes", "primary_customer_default", "customer_id", "customer_label", "customer_visibility", "job_links_json", "job_links_visibility", "archived"}
}
func (Project) Columns() []string {
	return []string{"project_id", "name", "description", "status_id", "status_label", "status_category", "displayed_progress_percent", "retained_manual_progress_percent", "planned_start_date", "planned_end_date", "service_notes", "notes", "legacy_review_reasons_json", "legacy_captured_timezone", "custom_fields_json", "customer_id", "customer_label", "customer_visibility", "location_id", "location_label", "location_visibility", "archived"}
}
func (Contact) Validate(raw json.RawMessage) error  { return validateMasterOptions(raw, "contact") }
func (Location) Validate(raw json.RawMessage) error { return validateMasterOptions(raw, "location") }
func (Project) Validate(raw json.RawMessage) error  { return validateMasterOptions(raw, "project") }
func (Contact) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeMaster(p, raw, "contact")
}
func (Location) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeMaster(p, raw, "location")
}
func (Project) Authorize(p authorization.Principal, raw json.RawMessage) error {
	return authorizeMaster(p, raw, "project")
}
func (Contact) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return authorizeMasterSelection(ctx, tx, p, raw, "contact")
}
func (Location) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return authorizeMasterSelection(ctx, tx, p, raw, "location")
}
func (Project) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	return authorizeMasterSelection(ctx, tx, p, raw, "project")
}
func (Contact) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotMaster(ctx, tx, p, raw, "contact", emit)
}
func (Location) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotMaster(ctx, tx, p, raw, "location", emit)
}
func (Project) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	return snapshotMaster(ctx, tx, p, raw, "project", emit)
}
func (Contact) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, ref json.RawMessage) error {
	return recheckMaster(ctx, tx, p, ref, "contact")
}
func (Location) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, ref json.RawMessage) error {
	return recheckMaster(ctx, tx, p, ref, "location")
}
func (Project) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, ref json.RawMessage) error {
	return recheckMaster(ctx, tx, p, ref, "project")
}

func masterOptions(raw json.RawMessage) (MasterRecordOptions, error) {
	var o MasterRecordOptions
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(&o) != nil || d.Decode(new(any)) != io.EOF {
		return o, ErrConflict
	}
	if o.CustomerID < 0 || o.StatusID < 0 || (o.Sort != "name" && o.Sort != "name_desc" && o.Sort != "updated") {
		return o, ErrConflict
	}
	return o, nil
}

func validateMasterOptions(raw json.RawMessage, resource string) error {
	o, err := masterOptions(raw)
	if err != nil {
		return err
	}
	if resource == "project" && o.CustomerID != 0 || resource != "project" && o.StatusID != 0 {
		return ErrConflict
	}
	return nil
}

func authorizeMaster(p authorization.Principal, raw json.RawMessage, resource string) error {
	if err := validateMasterOptions(raw, resource); err != nil {
		return err
	}
	o, err := masterOptions(raw)
	if err != nil {
		return err
	}
	if !p.Allows(resource+".export", authorization.All) || !p.Allows(resource+".view", authorization.All) || o.Archived && !p.Allows(resource+".archived.view", authorization.All) {
		return authorization.ErrForbidden
	}
	if o.CustomerID != 0 && !p.Allows("customer.view", authorization.All) {
		return authorization.ErrForbidden
	}
	return nil
}

// Explicit selected inputs remain authorized even when no rows match, through
// the lifecycle's selectionAuthorizer hook at acceptance, status and download.
func authorizeMasterSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, resource string) error {
	if err := authorizeMaster(p, raw, resource); err != nil {
		return err
	}
	o, _ := masterOptions(raw)
	var visible bool
	if o.CustomerID != 0 {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, o.CustomerID, p.CompanyID, o.Archived && p.Allows("customer.archived.view", authorization.All)).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return authorization.ErrForbidden
		}
	}
	if o.StatusID != 0 {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.object_type='project' AND (s.company_id=$2 OR s.company_id IS NULL) AND (w.company_id=$2 OR w.company_id IS NULL))`, o.StatusID, p.CompanyID).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return authorization.ErrForbidden
		}
	}
	return nil
}

type masterRowReference struct {
	ID       int64              `json:"id"`
	Archived bool               `json:"archived"`
	Customer *masterDisclosure  `json:"customer,omitempty"`
	Location *masterDisclosure  `json:"location,omitempty"`
	Jobs     []masterDisclosure `json:"jobs,omitempty"`
}
type masterDisclosure struct {
	ID       int64 `json:"id"`
	Archived bool  `json:"archived"`
}

// These fragments are private constants, never request-provided identifiers.
func masterTable(resource string) string {
	switch resource {
	case "contact":
		return "customer_contacts"
	case "location":
		return "locations"
	case "job":
		return "jobs"
	default:
		return "projects"
	}
}

func snapshotMaster(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, resource string, emit func(Record) error) error {
	if err := authorizeMasterSelection(ctx, tx, p, raw, resource); err != nil {
		return err
	}
	o, _ := masterOptions(raw)
	name, search, owner, where := "m.name", "(strpos(lower(m.name),lower($3))>0 OR strpos(lower(m.description),lower($3))>0)", "m.customer_id", ""
	values := `m.id::text,m.name,m.description,coalesce(m.status_id::text,''),coalesce(s.name,''),coalesce(s.category_key,''),CASE WHEN s.category_key='project:completed' THEN '100' ELSE m.completion_percentage::text END,m.completion_percentage::text,coalesce(to_char(m.planned_start_date,'YYYY-MM-DD'),''),coalesce(to_char(m.planned_end_date,'YYYY-MM-DD'),''),m.service_notes,m.notes,coalesce(to_jsonb(r.review_reasons),'[]'::jsonb)::text,coalesce(r.captured_timezone,''),jsonb_build_object('values',m.custom_fields,'definitions',coalesce((SELECT jsonb_agg(jsonb_build_object('id',d.id,'name',d.name,'field_type',d.field_type,'precision',d.precision,'retired',d.retired,'options',d.options::jsonb,'retired_options',d.retired_options) ORDER BY d.id) FROM custom_field_definitions d WHERE d.object_type='project' AND custom_field_applies_to_company(d.company_id,m.company_id) AND EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(m.custom_fields)='array' THEN m.custom_fields ELSE '[]'::jsonb END) v WHERE v->>'definition_id'=d.id::text)),'[]'::jsonb))::text`
	joins := ` LEFT JOIN statuses s ON s.id=m.status_id LEFT JOIN project_legacy_review r ON r.project_id=m.id LEFT JOIN locations l ON l.id=m.location_id AND l.company_id=m.company_id AND l.object_type='customer' AND l.object_id=m.customer_id`
	if resource == "contact" {
		// Match CustomerChild.Name's Unicode TrimSpace and the child list's
		// joined name/email/phone/address fields, including empty address slots.
		contactName := `btrim(m.first_name||' '||m.last_name,U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')`
		name, search = "concat_ws(' ',m.first_name,m.last_name)", "strpos(lower(concat_ws(' ',"+contactName+",m.email,m.phone,'','','','','')),lower($3))>0"
		values = `m.id::text,m.first_name,m.last_name,m.role_title,m.email,m.phone,m.service_notes,m.notes,m.is_primary::text`
		joins = ""
	} else if resource == "location" {
		name, search, owner, where = "m.title", "strpos(lower(concat_ws(' ',m.title,'','',m.address_1,m.address_2,m.city,m.state,m.zip_code)),lower($3))>0", "m.object_id", " AND m.object_type='customer'"
		values = `m.id::text,m.title,m.address_1,m.address_2,m.city,m.state,m.zip_code,coalesce(m.latitude::text,''),coalesce(m.longitude::text,''),m.access_notes,m.service_notes,m.notes,m.is_primary::text`
		joins = ""
	}
	// IDs and labels use exactly the same disclosure predicate. Stored references
	// contain only disclosed relationships, so unrelated grant loss cannot block.
	cVisible := `($4 AND c.id IS NOT NULL AND (c.deleted_at IS NULL OR $5))`
	values += `,CASE WHEN ` + cVisible + ` THEN c.id::text ELSE '' END,CASE WHEN ` + cVisible + ` THEN c.display_name ELSE '' END,CASE WHEN ` + cVisible + ` THEN CASE WHEN c.deleted_at IS NULL THEN 'visible' ELSE 'archived' END ELSE 'restricted' END`
	ref := `jsonb_build_object('id',m.id,'archived',m.deleted_at IS NOT NULL,'customer',CASE WHEN ` + cVisible + ` THEN jsonb_build_object('id',c.id,'archived',c.deleted_at IS NOT NULL) END)`
	args := []any{p.CompanyID, o.Archived, o.Search, p.Allows("customer.view", authorization.All), p.Allows("customer.archived.view", authorization.All)}
	if resource == "project" {
		lVisible := `($6 AND l.id IS NOT NULL AND (l.deleted_at IS NULL OR $7))`
		values += `,CASE WHEN ` + lVisible + ` THEN l.id::text ELSE '' END,CASE WHEN ` + lVisible + ` THEN l.title ELSE '' END,CASE WHEN m.location_id IS NULL THEN 'none' WHEN ` + lVisible + ` THEN CASE WHEN l.deleted_at IS NULL THEN 'visible' ELSE 'archived' END ELSE 'restricted' END`
		ref += ` || jsonb_build_object('location',CASE WHEN ` + lVisible + ` THEN jsonb_build_object('id',l.id,'archived',l.deleted_at IS NOT NULL) END)`
		args = append(args, p.Allows("location.view", authorization.All), p.Allows("location.archived.view", authorization.All))
	} else {
		// Explicit links alone establish this collection; a primary Customer
		// default never implies a Job link. EXISTS also deduplicates the legacy
		// single-contact mirror and the current many-contact relationship.
		linked := `j.location_id=m.id`
		if resource == "contact" {
			linked = `(j.customer_contact_id=m.id OR EXISTS(SELECT 1 FROM job_contacts jc WHERE jc.job_id=j.id AND jc.contact_id=m.id))`
		}
		joins += ` LEFT JOIN LATERAL (SELECT
		 coalesce(jsonb_agg(jsonb_build_object('id',j.id,'title',j.job_type,'archived',j.deleted_at IS NOT NULL) ORDER BY j.id),'[]'::jsonb) AS links,
		 coalesce(jsonb_agg(jsonb_build_object('id',j.id,'archived',j.deleted_at IS NOT NULL) ORDER BY j.id),'[]'::jsonb) AS refs
		 FROM jobs j WHERE $6 AND j.company_id=m.company_id AND j.customer_id=` + owner + ` AND ` + linked + ` AND (j.deleted_at IS NULL OR $7)) jl ON true`
		values += `,jl.links::text,CASE WHEN NOT $6 THEN 'restricted' WHEN $7 THEN 'visible' ELSE 'active_only' END`
		ref += ` || jsonb_build_object('jobs',jl.refs)`
		args = append(args, p.Allows("job.view", authorization.All), o.Archived && p.Allows("job.archived.view", authorization.All))
	}
	if resource == "project" {
		where += ` AND ($8::bigint=0 OR m.status_id=$8)`
		args = append(args, o.StatusID)
	} else {
		where += ` AND ($8::bigint=0 OR ` + owner + `=$8)`
		args = append(args, o.CustomerID)
	}
	values += `,(m.deleted_at IS NOT NULL)::text`
	order := "lower(" + name + "),m.id"
	if o.Sort == "name_desc" {
		order = "lower(" + name + ") DESC,m.id DESC"
	}
	if o.Sort == "updated" {
		order = "m.updated_at DESC,m.id DESC"
	}
	_, err := tx.Exec(ctx, `DECLARE master_record_export_snapshot NO SCROLL CURSOR FOR SELECT ARRAY[`+values+`],jsonb_strip_nulls(`+ref+`) FROM `+masterTable(resource)+` m LEFT JOIN customers c ON c.id=`+owner+` AND c.company_id=m.company_id`+joins+` WHERE m.company_id=$1 AND ($2 OR m.deleted_at IS NULL)`+where+` AND ($3='' OR `+search+`) ORDER BY `+order, args...)
	if err != nil {
		return err
	}
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 128 FROM master_record_export_snapshot`)
		if err != nil {
			return err
		}
		batch := []Record{}
		for rows.Next() {
			var record Record
			if err := rows.Scan(&record.Values, &record.Reference); err != nil {
				rows.Close()
				return err
			}
			// Fail the whole generation rather than truncate retained JSON or any
			// other cell. Together with cursor batches this bounds buffered output.
			if err := validateMasterCells(record.Values); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, record)
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
			if err := emit(record); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `CLOSE master_record_export_snapshot`)
	return err
}

const masterCellLimit = 1 << 20

func validateMasterCells(values []string) error {
	for _, value := range values {
		if len(value) > masterCellLimit {
			return ErrConflict
		}
	}
	return nil
}

func recheckMaster(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, resource string) error {
	var ref masterRowReference
	if json.Unmarshal(raw, &ref) != nil || ref.ID <= 0 {
		return ErrConflict
	}
	if !p.Allows(resource+".export", authorization.All) {
		return authorization.ErrForbidden
	}
	if err := recheckMasterIdentity(ctx, tx, p, resource, masterDisclosure{ID: ref.ID, Archived: ref.Archived}); err != nil {
		return err
	}
	if ref.Customer != nil {
		if err := recheckMasterIdentity(ctx, tx, p, "customer", *ref.Customer); err != nil {
			return err
		}
	}
	if ref.Location != nil {
		if err := recheckMasterIdentity(ctx, tx, p, "location", *ref.Location); err != nil {
			return err
		}
	}
	for _, job := range ref.Jobs {
		if err := recheckMasterIdentity(ctx, tx, p, "job", job); err != nil {
			return err
		}
	}
	return nil
}

func recheckMasterIdentity(ctx context.Context, tx pgx.Tx, p authorization.Principal, resource string, ref masterDisclosure) error {
	archived := p.Allows(resource+".archived.view", authorization.All)
	if ref.ID <= 0 || !p.Allows(resource+".view", authorization.All) || ref.Archived && !archived {
		return authorization.ErrForbidden
	}
	table, where := masterTable(resource), ""
	if resource == "customer" {
		table = "customers"
	}
	if resource == "location" {
		where = " AND object_type='customer'"
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3)`+where+`)`, ref.ID, p.CompanyID, archived).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return authorization.ErrForbidden
	}
	return nil
}

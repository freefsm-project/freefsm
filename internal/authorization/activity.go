package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ActivityFilter is shared by interactive history and durable exports. Dates
// are resolved to company-timezone instants at the HTTP boundary.
type ActivityFilter struct {
	NoMatch              bool       `json:"-"`
	ExportScope          Scope      `json:"export_scope,omitempty"`
	ProvenanceEstimateID int64      `json:"provenance_estimate_id,omitempty"`
	Types                []string   `json:"types,omitempty"`
	Actions              []string   `json:"actions,omitempty"`
	RecordID             int64      `json:"record_id,omitempty"`
	ActorID              int64      `json:"actor_id,omitempty"`
	From                 *time.Time `json:"from,omitempty"`
	Until                *time.Time `json:"until,omitempty"`
}

type ActivityFact struct {
	Field       string `json:"field"`
	Before      string `json:"before"`
	After       string `json:"after"`
	BeforeKnown bool   `json:"before_known"`
	AfterKnown  bool   `json:"after_known"`
}

// ActivityRecord is an authorized projection, never a raw audit payload.
type ActivityRecord struct {
	ServiceJobID         int64          `json:"service_job_id,omitempty"`
	ServiceAssetID       int64          `json:"service_asset_id,omitempty"`
	SourceKey            string         `json:"-"`
	ProvenanceEstimateID int64          `json:"provenance_estimate_id,omitempty"`
	ExportScope          Scope          `json:"export_scope,omitempty"`
	ID                   int64          `json:"id"`
	ActorID              int64          `json:"actor_id"`
	Actor                string         `json:"actor"`
	Type                 string         `json:"type"`
	RecordID             int64          `json:"record_id"`
	Name                 string         `json:"name"`
	Action               string         `json:"action"`
	Accepted             time.Time      `json:"accepted"`
	Occurred             *time.Time     `json:"occurred,omitempty"`
	Facts                []ActivityFact `json:"facts"`
	Facets               []string       `json:"facets"`
	Archived             bool           `json:"archived"`
}

var activityTables = map[string]string{"customer": "customers", "contact": "customer_contacts", "location": "locations", "asset": "assets", "project": "projects", "job": "jobs", "estimate": "estimates", "invoice": "invoices", "item": "items", "time_entry": "time_entries"}

func ValidActivityType(typ string) bool { _, ok := activityTables[typ]; return ok }

// CheckActivitySelection validates explicit inputs on the caller's authority
// transaction, including empty snapshots; a known record ID is not authority.
func CheckActivitySelection(ctx context.Context, tx pgx.Tx, p Principal, f ActivityFilter) error {
	if f.ProvenanceEstimateID > 0 {
		return activityProvenanceAccess(ctx, tx, p, f.ProvenanceEstimateID, f.ExportScope)
	}
	if f.RecordID == 0 {
		return nil
	}
	if len(f.Types) != 1 {
		return ErrConflict
	}
	_, job, _, _, err := ActivityTarget(ctx, tx, p, f.Types[0], f.RecordID)
	if err != nil {
		return err
	}
	if f.ExportScope != "" {
		allowed, err := activityFacet(ctx, tx, p, "activity.export", job, false)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		if f.ExportScope == Assigned {
			assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, false)
			if err != nil {
				return err
			}
			if job == 0 || !assigned {
				return ErrForbidden
			}
		}
	}
	return nil
}

func ValidateActivityFilter(f ActivityFilter) error {
	if f.ExportScope != "" && f.ExportScope != All && f.ExportScope != Assigned {
		return ErrConflict
	}
	if f.ActorID < 0 || f.RecordID < 0 || f.RecordID > 0 && len(f.Types) != 1 || f.From != nil && f.Until != nil && !f.From.Before(*f.Until) {
		return ErrConflict
	}
	for _, typ := range f.Types {
		if !ValidActivityType(typ) {
			return ErrConflict
		}
	}
	for _, action := range f.Actions {
		if action == "" || len(action) > 100 {
			return ErrConflict
		}
	}
	return nil
}

// ActivityTarget uses current canonical View/archived/assignment/Own anchors.
// Closed Job Context never grants a master activity collection.
func ActivityTarget(ctx context.Context, tx pgx.Tx, p Principal, typ string, id int64) (name string, job int64, own, archived bool, err error) {
	table, ok := activityTables[typ]
	if !ok {
		err = ErrNotFound
		return
	}
	var row map[string]json.RawMessage
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT to_jsonb(t) FROM `+table+` t WHERE id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
		return
	}
	if err != nil {
		return
	}
	if err = json.Unmarshal(raw, &row); err != nil {
		return
	}
	var company int64
	_ = json.Unmarshal(row["company_id"], &company)
	if company != p.CompanyID && !(typ == "item" && company == 0) {
		err = ErrNotFound
		return
	}
	if typ == "location" {
		var ownerType string
		_ = json.Unmarshal(row["object_type"], &ownerType)
		if ownerType != "customer" {
			err = ErrNotFound
			return
		}
	}
	if string(row["conversion_hidden_at"]) != "" && string(row["conversion_hidden_at"]) != "null" {
		err = ErrNotFound
		return
	}
	archived = string(row["deleted_at"]) != "" && string(row["deleted_at"]) != "null"
	permissionType := typ
	if typ == "time_entry" {
		permissionType = "time"
		var subject int64
		_ = json.Unmarshal(row["user_id"], &subject)
		own = subject == p.UserID
	}
	_ = json.Unmarshal(row["job_id"], &job)
	if typ == "job" {
		job = id
	}
	allowed := p.Allows(permissionType+".view", All) || own && p.Allows(permissionType+".view", Own)
	if !allowed && job > 0 && p.Allows(permissionType+".view", Assigned) {
		allowed, err = assignedJobAccess(ctx, tx, p.UserID, job, false)
		if err != nil {
			return
		}
	}
	if !allowed || archived && !p.Allows(permissionType+".archived.view", All) {
		err = ErrNotFound
		return
	}
	for _, key := range []string{"display_name", "name", "title", "subtitle", "job_type", "number"} {
		var s string
		_ = json.Unmarshal(row[key], &s)
		if s != "" {
			name = s
			break
		}
	}
	if name == "" {
		if typ == "contact" {
			var first, last string
			_ = json.Unmarshal(row["first_name"], &first)
			_ = json.Unmarshal(row["last_name"], &last)
			name = strings.TrimSpace(first + " " + last)
		}
	}
	if name == "" {
		name = fmt.Sprintf("%s #%d", typ, id)
	}
	return
}

func activityFacet(ctx context.Context, tx pgx.Tx, p Principal, permission string, job int64, own bool) (bool, error) {
	if p.Allows(permission, All) || own && p.Allows(permission, Own) {
		return true, nil
	}
	if job > 0 && p.Allows(permission, Assigned) {
		return assignedJobAccess(ctx, tx, p.UserID, job, false)
	}
	return false, nil
}

// WalkActivity filters every row and field before emit. Batches bound memory;
// caller owns the transaction/snapshot and may stop after enough visible rows.
// Export callers use the #96 repeatable-read authority fence.
func WalkActivity(ctx context.Context, tx pgx.Tx, p Principal, f ActivityFilter, export bool, emit func(ActivityRecord) (bool, error)) error {
	if err := ValidateActivityFilter(f); err != nil {
		return err
	}
	if f.NoMatch {
		return nil
	}
	if f.ProvenanceEstimateID > 0 {
		if err := activityProvenanceAccess(ctx, tx, p, f.ProvenanceEstimateID, f.ExportScope); err != nil {
			return err
		}
	}
	var lastTime *time.Time
	var lastID int64
	for {
		args := []any{p.CompanyID, append([]string{}, f.Types...), append([]string{}, f.Actions...), f.RecordID, f.ActorID, f.From, f.Until, lastTime, lastID}
		lineage := ""
		if f.ProvenanceEstimateID > 0 {
			args = append(args, f.ProvenanceEstimateID)
			lineage = ` AND ((object_type='estimate' AND object_id=$10) OR (object_type='invoice' AND object_id IN(SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE company_id=$1 AND estimate_id=$10)))`
		}
		rows, err := tx.Query(ctx, `SELECT id,coalesce(actor_id,0),object_type,object_id,action,metadata,created_at,coalesce(event_key,'') FROM activity_logs WHERE company_id=$1 AND (cardinality($2::text[])=0 OR object_type=ANY($2)) AND (cardinality($3::text[])=0 OR action=ANY($3)) AND ($4::bigint=0 OR object_id=$4) AND ($5::bigint=0 OR actor_id=$5) AND ($6::timestamptz IS NULL OR created_at >= $6) AND ($7::timestamptz IS NULL OR created_at < $7) AND ($8::timestamptz IS NULL OR (created_at,id)<($8,$9))`+lineage+` ORDER BY created_at DESC,id DESC LIMIT 128`, args...)
		if err != nil {
			return err
		}
		type candidate struct {
			event    ActivityRecord
			metadata []byte
		}
		batch := []candidate{}
		for rows.Next() {
			var c candidate
			e := &c.event
			if err := rows.Scan(&e.ID, &e.ActorID, &e.Type, &e.RecordID, &e.Action, &c.metadata, &e.Accepted, &e.SourceKey); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, c := range batch {
			lastID = c.event.ID
			stamp := c.event.Accepted
			lastTime = &stamp
			c.event.ExportScope = f.ExportScope
			e, err := projectActivity(ctx, tx, p, c.event, c.metadata, export)
			if errors.Is(err, ErrNotFound) && f.ProvenanceEstimateID > 0 && slices.Contains([]string{"converted", "estimate_converted", "invoice_conversion_reverted"}, c.event.Action) {
				e = c.event
				e.Facts = []ActivityFact{}
				e.Facets = []string{}
				e.ProvenanceEstimateID = f.ProvenanceEstimateID
				e.Name = fmt.Sprintf("Historical %s #%d (provenance only)", e.Type, e.RecordID)
				var metadata map[string]json.RawMessage
				_ = json.Unmarshal(c.metadata, &metadata)
				e.Actor = activityActorLabel(e.ActorID, metadata)
				err = nil
			}
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				continue
			}
			if err != nil {
				return err
			}
			more, err := emit(e)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
	}
}

func activityProvenanceAccess(ctx context.Context, tx pgx.Tx, p Principal, root int64, exportScope Scope) error {
	type ref struct {
		typ string
		id  int64
	}
	refs := []ref{{"estimate", root}}
	rows, err := tx.Query(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE company_id=$1 AND estimate_id=$2`, p.CompanyID, root)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref{"invoice", id})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range refs {
		_, job, _, _, err := ActivityTarget(ctx, tx, p, r.typ, r.id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		ok, err := activityFacet(ctx, tx, p, r.typ+".provenance.view", job, false)
		if err != nil {
			return err
		}
		if ok {
			if exportScope != "" {
				allowed, err := activityFacet(ctx, tx, p, "activity.export", job, false)
				if err != nil {
					return err
				}
				if !allowed {
					continue
				}
				if exportScope == Assigned {
					assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, false)
					if err != nil {
						return err
					}
					if job == 0 || !assigned {
						continue
					}
				}
			}
			return nil
		}
	}
	return ErrNotFound
}

func projectActivity(ctx context.Context, tx pgx.Tx, p Principal, e ActivityRecord, raw []byte, export bool) (ActivityRecord, error) {
	// A legacy event may have been attached to the wrong parent. Parent View
	// never declassifies a known security, backup or open-tracking event.
	switch e.Action {
	case "password_reset", "password_changed", "admin_password_recovered", "logged_in", "logged_out", "user_created", "user_updated", "user_disabled", "user_enabled", "welcome_resent", "welcome_invite_sent", "invite_accepted", "settings_updated", "document_delivery_opened", "document_opened", "email_opened", "delivery.opened", "open_tracking_changed", "restore_started", "restore_completed", "restore_failed":
		return e, ErrNotFound
	}
	if strings.HasPrefix(e.Action, "backup_") {
		return e, ErrNotFound
	}
	e.Facts = []ActivityFact{}
	e.Facets = []string{}
	name, job, own, archived, err := ActivityTarget(ctx, tx, p, e.Type, e.RecordID)
	if err != nil {
		return e, err
	}
	e.Name = name
	e.Archived = archived
	var meta map[string]json.RawMessage
	if strings.HasPrefix(e.Action, "service_entry_") {
		if !p.Allows("service_entry.view", All) && !p.Allows("service_entry.view", Assigned) {
			return e, ErrForbidden
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			if !p.Allows("service_entry.view", All) {
				return e, ErrForbidden
			}
			return e, err
		}
		_ = json.Unmarshal(meta["asset_id"], &e.ServiceAssetID)
		_ = json.Unmarshal(meta["job_id"], &e.ServiceJobID)
		if e.Type == "job" {
			e.ServiceJobID = e.RecordID
		}
		if e.Type == "asset" {
			e.ServiceAssetID = e.RecordID
		}
		if !p.Allows("service_entry.view", All) || e.ExportScope == Assigned {
			if err := currentServiceActivityAsset(ctx, tx, p, e.ServiceJobID, e.ServiceAssetID); err != nil {
				return e, err
			}
		}
		if e.ServiceJobID > 0 {
			job = e.ServiceJobID
		}
	}
	if export {
		if e.ExportScope == Assigned {
			assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, false)
			if err != nil {
				return e, err
			}
			if job == 0 || !assigned {
				return e, ErrForbidden
			}
		}
		ok, err := activityFacet(ctx, tx, p, "activity.export", job, false)
		if err != nil {
			return e, err
		}
		if !ok {
			return e, ErrForbidden
		}
	}
	if meta == nil {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return e, err
		}
	}
	e.Actor = activityActorLabel(e.ActorID, meta)
	executionSubtask := e.Type == "job" && strings.HasPrefix(e.SourceKey, "authorization-audit:") && meta["subtask_id"] != nil
	var channel string
	_ = json.Unmarshal(meta["channel"], &channel)
	if e.Action == "document_delivery_accepted" || strings.HasPrefix(e.SourceKey, "authorization-audit:") && channel == "worker" {
		e.Actor = "System (initiated by " + e.Actor + ")"
	}
	if strings.HasPrefix(e.SourceKey, "authorization-audit:") || strings.HasPrefix(e.SourceKey, "file-action:") {
		if value, ok := activityScalar(meta["source_occurred_at"]); ok {
			if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil && !stamp.After(e.Accepted) {
				e.Occurred = &stamp
			}
		}
	}
	// The receipt-backed Status writer predates keyed source events. Its
	// supplied occurrence is already recorded on the immutable operational row.
	// Read that exact contract without rewriting history or consulting an editor.
	if e.Type == "job" && e.Action == "status_transitioned" {
		if value, ok := activityScalar(meta["occurred_at"]); ok {
			if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil && !stamp.After(e.Accepted) {
				e.Occurred = &stamp
			}
		}
	}
	// Only reviewed action/facet mappings disclose operational data. Unknown
	// historic actions retain identity/time but never arbitrary metadata.
	required := ""
	switch {
	case (e.Type == "job" || e.Type == "time_entry") && (strings.HasPrefix(e.Action, "gps_") || strings.HasPrefix(e.Action, "time_gps_")):
		required = "time.gps.view"
	case strings.HasPrefix(e.Action, "contact_"):
		required = "contact.view"
	case strings.HasPrefix(e.Action, "location_"):
		required = "location.view"
	case strings.HasPrefix(e.Action, "service_entry_"):
		required = "service_entry.view"
	case strings.HasPrefix(e.Action, "payment_") || strings.HasPrefix(e.Action, "credit_") || strings.HasPrefix(e.Action, "refund_") || strings.HasPrefix(e.Action, "settlement_"):
		required = "settlement.view"
	case activityProvenanceFacet(e.Type, e.Action) != "":
		required = activityProvenanceFacet(e.Type, e.Action)
	case strings.HasPrefix(e.Action, "clocked_") && e.Type == "job":
		required = "time.view"
	case strings.HasPrefix(e.Action, "cost_"):
		required = "cost.view"
	case strings.HasPrefix(e.Action, "pricebook_"):
		required = "pricebook.view"
	}
	if required != "" {
		ok, err := activityFacet(ctx, tx, p, required, job, own)
		if err != nil {
			return e, err
		}
		if !ok {
			return e, ErrForbidden
		}
		e.Facets = append(e.Facets, required)
	}
	pairs := [][3]string{}
	switch e.Action {
	case "status_changed", "status_transitioned":
		pairs = append(pairs, [3]string{"Status", "old_status", "new_status"})
		pairs = append(pairs, [3]string{"Status ID", "from_status_id", "to_status_id"})
	case "scheduled", "rescheduled", "dispatched":
		pairs = append(pairs, [3]string{"Schedule start", "old_start", "new_start"}, [3]string{"Schedule end", "old_end", "new_end"})
	case "field_notes_updated", "field_notes_changed", "tech_notes_updated":
		pairs = append(pairs, [3]string{"Field Notes", "before", "after"})
	case "subtask_completed", "subtask_uncompleted":
		if executionSubtask {
			break // Structured dedicated history is projected below.
		}
		pairs = append(pairs, [3]string{"Subtask", "old_title", "title"}, [3]string{"Completed", "before", "after"})
		if _, ok := meta["title"]; !ok {
			meta["title"] = meta["subtask_title"]
			if len(meta["title"]) == 0 {
				meta["title"] = meta["entity_name"]
			}
		}
		if _, ok := meta["after"]; !ok {
			meta["after"] = json.RawMessage("true")
			if e.Action == "subtask_uncompleted" {
				meta["after"] = json.RawMessage("false")
			}
		}
	case "service_entry_created", "service_entry_revised":
		pairs = append(pairs, [3]string{"Service Entry summary", "before", "after"})
	}
	for _, pair := range pairs {
		before, bok := activityScalar(meta[pair[1]])
		after, aok := activityScalar(meta[pair[2]])
		if bok || aok {
			e.Facts = append(e.Facts, ActivityFact{pair[0], before, after, bok, aok})
		}
	}
	if executionSubtask {
		switch e.Action {
		case "subtask_added", "subtask_renamed", "subtask_completed", "subtask_uncompleted", "subtask_removed":
			before, after := activityObject(meta["before"]), activityObject(meta["after"])
			if after != nil {
				for _, field := range [][2]string{{"Subtask ID", "id"}, {"Subtask title", "title"}, {"Order", "sort_order"}, {"Completed", "completed"}, {"Completion revision", "completion_revision"}, {"Completion actor", "completion_actor"}, {"Completion accepted at", "completion_at"}, {"Removed at", "removed_at"}} {
					b, bok := activityScalar(before[field[1]])
					a, aok := activityScalar(after[field[1]])
					if bok || aok {
						e.Facts = append(e.Facts, ActivityFact{field[0], b, a, bok, aok})
					}
				}
			}
		case "subtasks_reordered":
			// Older reorder history stores full before rows and ordered after
			// identities. Retain only active identities and their recorded order;
			// removed rows are not members of the reordered checklist.
			var rows []struct {
				ID      string     `json:"id"`
				Order   int        `json:"sort_order"`
				Removed *time.Time `json:"removed_at"`
			}
			var ids []string
			if json.Unmarshal(meta["before"], &rows) == nil && json.Unmarshal(meta["after"], &ids) == nil {
				type position struct {
					ID    string `json:"id"`
					Order int    `json:"sort_order"`
				}
				before, after := []position{}, []position{}
				for _, row := range rows {
					if row.Removed == nil {
						before = append(before, position{row.ID, row.Order})
					}
				}
				for i, id := range ids {
					after = append(after, position{id, i})
				}
				b, _ := json.Marshal(before)
				a, _ := json.Marshal(after)
				e.Facts = append(e.Facts, ActivityFact{"Subtask order", string(b), string(a), true, true})
			}
		}
	}
	if strings.HasPrefix(e.Action, "service_entry_") {
		before, after := activityObject(meta["before"]), activityObject(meta["after"])
		for _, key := range []string{"summary", "service_date"} {
			b, bok := activityScalar(before[key])
			a, aok := activityScalar(after[key])
			if bok || aok {
				e.Facts = append(e.Facts, ActivityFact{key, b, a, bok, aok})
			}
		}
		b, bok := activityRecordedNames(before["responsible_names"])
		a, aok := activityRecordedNames(after["responsible_names"])
		if bok || aok {
			e.Facts = append(e.Facts, ActivityFact{"Responsible participants", b, a, bok, aok})
		}
	}
	if e.Action == "job_fields_changed" {
		before, after := activityFieldValues(meta["before"]), activityFieldValues(meta["after"])
		ids := []int64{}
		for id := range before {
			ids = append(ids, id)
		}
		for id := range after {
			if _, ok := before[id]; !ok {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			if before[id] == after[id] {
				continue
			}
			label := fmt.Sprintf("Custom field #%d", id)
			var currentLabel string
			if err := tx.QueryRow(ctx, `SELECT name FROM custom_field_definitions WHERE id=$1 AND object_type='job' AND (company_id=$2 OR company_id IS NULL)`, id, p.CompanyID).Scan(&currentLabel); err == nil {
				label = currentLabel
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return e, err
			}
			_, bok := before[id]
			_, aok := after[id]
			e.Facts = append(e.Facts, ActivityFact{label, before[id], after[id], bok, aok})
		}
	}
	// Before/after objects are projected by a closed field catalog. A parent
	// grant is never permission to serialize a mixed-facet historical payload.
	before, after := activityObject(meta["before"]), activityObject(meta["after"])
	if executionSubtask || !slices.Contains([]string{"created", "updated", "archived", "restored", "status_changed", "status_transitioned", "field_notes_updated", "field_notes_changed", "tech_notes_updated", "subtask_completed", "subtask_uncompleted", "corrected", "voided", "pricebook_created", "pricebook_updated", "cost_updated"}, e.Action) {
		before = nil
		after = nil
	}
	fields := activityFields(e.Type)
	hiddenFacts := false
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b, bok := activityScalar(before[key])
		a, aok := activityScalar(after[key])
		if !bok && !aok || bok && aok && b == a {
			continue
		}
		facet := fields[key]
		if facet != "" {
			facetJob, facetOwn := job, own
			if activityCommercialAnchor(e.Type, key) != "" {
				// Current assignment cannot authorize a different historical
				// anchor. Match CommercialHistory's independent broad Views.
				facetJob, facetOwn = 0, false
			}
			allowed, err := activityFacet(ctx, tx, p, facet, facetJob, facetOwn)
			if err != nil {
				return e, err
			}
			if !allowed {
				hiddenFacts = true
				continue
			}
			if !slices.Contains(e.Facets, facet) {
				e.Facets = append(e.Facets, facet)
			}
		}
		e.Facts = append(e.Facts, ActivityFact{key, b, a, bok, aok})
		if e.Type == "asset" && (key == "asset_type_id" || key == "asset_status_id") {
			left, lok, err := activityAssetVocabulary(ctx, tx, p, key, before[key])
			if err != nil {
				return e, err
			}
			right, rok, err := activityAssetVocabulary(ctx, tx, p, key, after[key])
			if err != nil {
				return e, err
			}
			if lok || rok {
				e.Facts = append(e.Facts, ActivityFact{key + " (current vocabulary)", left, right, lok, rok})
			}
		}
	}
	if e.Type == "job" || e.Type == "estimate" || e.Type == "invoice" {
		b, bok := activityStructuredArray(before["line_items"], []string{"title", "description", "unit_price", "quantity", "taxable", "tax_rate", "discount", "surcharge"})
		a, aok := activityStructuredArray(after["line_items"], []string{"title", "description", "unit_price", "quantity", "taxable", "tax_rate", "discount", "surcharge"})
		if (bok || aok) && a != b {
			allowed, err := activityFacet(ctx, tx, p, "pricing.view", job, false)
			if err != nil {
				return e, err
			}
			if allowed {
				e.Facts = append(e.Facts, ActivityFact{"Line items", b, a, bok, aok})
				if !slices.Contains(e.Facets, "pricing.view") {
					e.Facets = append(e.Facets, "pricing.view")
				}
			} else {
				hiddenFacts = true
			}
		}
		if e.Type == "job" {
			b, bok := activityStructuredArray(before["subtasks"], []string{"id", "title", "completed", "completed_by", "completed_at"})
			a, aok := activityStructuredArray(after["subtasks"], []string{"id", "title", "completed", "completed_by", "completed_at"})
			if (bok || aok) && a != b {
				e.Facts = append(e.Facts, ActivityFact{"Subtasks", b, a, bok, aok})
			}
		}
	}
	// Typed custom values inherit parent View, including retained definitions.
	if e.Type != "job" {
		b, a := activityFieldValues(before["custom_fields"]), activityFieldValues(after["custom_fields"])
		ids := []int64{}
		for id := range b {
			ids = append(ids, id)
		}
		for id := range a {
			if _, ok := b[id]; !ok {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			if b[id] != a[id] {
				_, bok := b[id]
				_, aok := a[id]
				e.Facts = append(e.Facts, ActivityFact{fmt.Sprintf("Custom field #%d", id), b[id], a[id], bok, aok})
			}
		}
	}
	if required == "settlement.view" {
		facts, err := activitySettlementFacts(ctx, tx, p, e, meta)
		if err != nil {
			return e, err
		}
		e.Facts = append(e.Facts, facts...)
		for _, key := range []string{"amount", "amount_cents", "method", "effective_date", "reason"} {
			if value, ok := activityScalar(meta[key]); ok {
				e.Facts = append(e.Facts, ActivityFact{Field: key, After: value, AfterKnown: true})
			}
		}
	}
	if strings.HasPrefix(e.Action, "file_") {
		if e.Action == "file_renamed" {
			before, bok := activityScalar(meta["old_file_name"])
			after, aok := activityScalar(meta["file_name"])
			e.Facts = append(e.Facts, ActivityFact{"File name", before, after, bok, aok})
		}
		for _, key := range []string{"file_name", "size"} {
			if value, ok := activityScalar(meta[key]); ok {
				e.Facts = append(e.Facts, ActivityFact{Field: key, After: value, AfterKnown: true})
			}
		}
	}
	if e.Action == "clocked_in" || e.Action == "clocked_out" {
		key := "clock_in"
		if e.Action == "clocked_out" {
			key = "clock_out"
		}
		if value, ok := activityScalar(meta[key]); ok {
			if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil && !stamp.After(e.Accepted) {
				e.Occurred = &stamp
			}
		}
	}
	// Removed Comments and Files remain safe summaries. No preview/body/blob,
	// path, credentials, arbitrary nested maps or historical linked names escape.
	if hiddenFacts && len(e.Facts) == 0 {
		return e, ErrForbidden
	}
	return e, nil
}

func activityFieldValues(raw json.RawMessage) map[int64]string {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var values []struct {
		ID    int64           `json:"definition_id"`
		Value json.RawMessage `json:"value"`
	}
	_ = json.Unmarshal(raw, &values)
	result := map[int64]string{}
	for _, v := range values {
		if value, ok := activityScalar(v.Value); ok {
			result[v.ID] = value
			continue
		}
		var options []string
		if json.Unmarshal(v.Value, &options) == nil {
			result[v.ID] = strings.Join(options, ", ")
		}
	}
	return result
}

func activityActorLabel(id int64, metadata map[string]json.RawMessage) string {
	var name string
	_ = json.Unmarshal(metadata["actor_name"], &name)
	if name != "" {
		if id == 0 {
			return "Recorded author: " + name
		}
		return name
	}
	if id > 0 {
		return fmt.Sprintf("User #%d (historical name not recorded)", id)
	}
	return "Unknown historical actor"
}

func activityRecordedNames(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var names []string
	if json.Unmarshal(raw, &names) != nil {
		return "", false
	}
	encoded, err := json.Marshal(names)
	return string(encoded), err == nil
}

func activityStructuredArray(raw json.RawMessage, fields []string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var rows []map[string]json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return "", false
	}
	clean := make([]map[string]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		value := map[string]json.RawMessage{}
		for _, field := range fields {
			if _, ok := activityScalar(row[field]); ok {
				value[field] = row[field]
			}
		}
		clean = append(clean, value)
	}
	b, err := json.Marshal(clean)
	return string(b), err == nil
}

func activityObject(raw json.RawMessage) map[string]json.RawMessage {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var result map[string]json.RawMessage
	_ = json.Unmarshal(raw, &result)
	return result
}

func activityFields(typ string) map[string]string {
	fields := map[string]string{}
	fields["deleted_at"] = ""
	base := ""
	switch typ {
	case "customer":
		base = "display_name first_name last_name company_name account_type email phone notes service_notes"
		for _, key := range strings.Fields("billing_address_1 billing_address_2 billing_city billing_state billing_zip_code tax_exempt") {
			fields[key] = "customer.financial.view"
		}
	case "contact":
		base = "first_name last_name email phone role_title notes service_notes is_primary"
	case "location":
		base = "title address_1 address_2 city state zip_code country latitude longitude notes service_notes access_notes is_primary"
	case "job":
		base = "title job_type subtitle notes tech_notes field_notes status_id start_time end_time due_date arrival_window_start arrival_window_end"
		fields["billing_type"] = "pricing.view"
	case "estimate", "invoice":
		// Anchor IDs are recorded commercial facts, not links or authority to
		// resolve historical Customer/Job names or other inaccessible details.
		base = "title notes terms status_id invoice_date due_date valid_through"
		fields["customer_id"] = "customer.view"
		fields["job_id"] = "job.view"
		for _, key := range strings.Fields("tax_rate subtotal total discount surcharge amount_cents") {
			fields[key] = "pricing.view"
		}
	case "project":
		base = "name description notes service_notes status_id completion_percentage start_time end_time planned_start_date planned_end_date"
	case "asset":
		base = "name number ownership_class serial_number manufacturer model notes service_notes asset_type_id asset_status_id installed_at warranty_expires"
	case "item":
		base = "name description type"
		for _, key := range strings.Fields("sku unit_price tax_rate taxable") {
			fields[key] = "pricebook.view"
		}
		fields["unit_cost"] = "cost.view"
	case "time_entry":
		base = "user_id job_id clock_in clock_out notes voided_at reason"
		for _, key := range strings.Fields("latitude longitude accuracy captured_at") {
			fields[key] = "time.gps.view"
		}
	}
	for _, key := range strings.Fields(base) {
		fields[key] = ""
	}
	return fields
}

func activityScalar(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	switch v := v.(type) {
	case string:
		return v, true
	case bool:
		return fmt.Sprint(v), true
	case float64:
		return string(raw), true
	case nil:
		return "", true
	}
	return "", false
}

func activityAssetVocabulary(ctx context.Context, tx pgx.Tx, p Principal, key string, raw json.RawMessage) (string, bool, error) {
	var id int64
	if len(raw) == 0 || json.Unmarshal(raw, &id) != nil || id <= 0 {
		return "", false, nil
	}
	table := "asset_types"
	if key == "asset_status_id" {
		table = "asset_statuses"
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM `+table+` WHERE id=$1 AND (company_id=$2 OR company_id IS NULL)`, id, p.CompanyID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// RecheckActivity rejects a whole stored artifact on any lost parent/facet or
// archived snapshot permission. It never substitutes a current subset.
func RecheckActivity(ctx context.Context, tx pgx.Tx, p Principal, e ActivityRecord) error {
	if e.ProvenanceEstimateID > 0 {
		return activityProvenanceAccess(ctx, tx, p, e.ProvenanceEstimateID, e.ExportScope)
	}
	_, job, own, _, err := ActivityTarget(ctx, tx, p, e.Type, e.RecordID)
	if err != nil {
		return err
	}
	if strings.HasPrefix(e.Action, "service_entry_") {
		if !p.Allows("service_entry.view", All) || e.ExportScope == Assigned {
			if err := currentServiceActivityAsset(ctx, tx, p, e.ServiceJobID, e.ServiceAssetID); err != nil {
				return err
			}
		}
		if e.ServiceJobID > 0 {
			job = e.ServiceJobID
		}
	}
	if e.ExportScope == Assigned {
		assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, false)
		if err != nil {
			return err
		}
		if job == 0 || !assigned {
			return ErrForbidden
		}
	}
	if e.Archived && !p.Allows(e.Type+".archived.view", All) {
		return ErrForbidden
	}
	// Stored facts may outlive a broad-to-Assigned grant change. Re-derive
	// anchor authority from the actual exported fields, including old artifacts.
	for _, fact := range e.Facts {
		if permission := activityCommercialAnchor(e.Type, fact.Field); permission != "" && !p.Allows(permission, All) {
			return ErrForbidden
		}
	}
	facets := append(slices.Clone(e.Facets), "activity.export")
	// Re-derive action-level authority too: an older artifact may predate a
	// classification repair and cannot make a missing facet authoritative.
	if permission := activityProvenanceFacet(e.Type, e.Action); permission != "" {
		facets = append(facets, permission)
	}
	for _, permission := range facets {
		ok, err := activityFacet(ctx, tx, p, permission, job, own && permission != "activity.export")
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}

func activityCommercialAnchor(typ, field string) string {
	if typ == "estimate" || typ == "invoice" {
		switch field {
		case "customer_id":
			return "customer.view"
		case "job_id":
			return "job.view"
		}
	}
	return ""
}

// Actual conversion writers use these identities; "converted" is the retained
// legacy identity. Do not infer provenance classification from text fragments.
func activityProvenanceFacet(typ, action string) string {
	switch action {
	case "estimate_converted", "invoice_conversion_reverted", "converted":
		return typ + ".provenance.view"
	}
	return ""
}

func currentServiceActivityAsset(ctx context.Context, tx pgx.Tx, p Principal, job, asset int64) error {
	if job <= 0 || asset <= 0 || !p.Allows("asset.view", Context) {
		return ErrForbidden
	}
	var visible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id JOIN assets equipment ON equipment.id=j.asset_id AND equipment.company_id=j.company_id AND equipment.customer_id=j.customer_id AND equipment.deleted_at IS NULL WHERE j.id=$1 AND equipment.id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL AND a.user_id=$4)`, job, asset, p.CompanyID, p.UserID).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return ErrForbidden
	}
	return nil
}

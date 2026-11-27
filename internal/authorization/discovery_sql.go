package authorization

import (
	"fmt"
	"strings"
)

// All SQL identifiers and expressions below are an internal allowlist. User
// input is exclusively bound; strpos/left give literal, not LIKE, semantics.
type discoverySQL struct {
	p         Principal
	q         DiscoveryQuery
	args      []any
	collation string
}

func (b *discoverySQL) bind(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func (b *discoverySQL) build(types []string) (string, []any) {
	company := b.bind(b.p.CompanyID)
	search := b.bind(b.q.Search)
	var sources []string
	for _, typ := range types {
		sources = append(sources, b.source(typ, company))
	}
	order := "rank,typ,id"
	if b.q.Sort == "name" {
		order = b.fold("title") + ",typ,id"
	} else if b.q.Sort == "name_desc" {
		order = b.fold("title") + " DESC,typ,id"
	}
	limit, offset := b.bind(b.q.PerPage), b.bind((b.q.Page-1)*b.q.PerPage)
	query := `WITH candidates AS MATERIALIZED (` + strings.Join(sources, " UNION ALL ") + `),
 matched AS MATERIALIZED (
 SELECT c.*,m.rank,m.label FROM candidates c
 CROSS JOIN LATERAL (
  SELECT CASE WHEN t.identifier AND ` + b.fold("t.value") + `=` + b.fold(search) + ` THEN 0
    WHEN left(` + b.fold("t.value") + `,length(` + search + `))=` + b.fold(search) + ` THEN 1 ELSE 2 END rank,t.label
  FROM jsonb_to_recordset(c.terms) AS t(value text,identifier boolean,label text)
  WHERE ` + search + `='' OR strpos(` + b.fold("t.value") + `,` + b.fold(search) + `)>0
  ORDER BY rank,t.label,t.value LIMIT 1
 ) m WHERE c.filter_ok
 ), page AS (
 SELECT * FROM matched ORDER BY ` + order + ` LIMIT ` + limit + ` OFFSET ` + offset + `
 )
 SELECT (SELECT count(*) FROM matched),EXISTS(SELECT 1 FROM candidates),
 coalesce((SELECT jsonb_agg(jsonb_build_object(
 'Type',typ,'ID',id,'Identifier',identifier,'Title',title,'Description',description,
 'URL',url,'Match',CASE WHEN ` + search + `='' THEN '' ELSE label END,'Archived',archived,'Status',status)
 ORDER BY ` + order + `) FROM page),'[]'::jsonb),statement_timestamp()`
	return query, b.args
}

func (b *discoverySQL) fold(expression string) string {
	// collation comes only from the fixed pg_catalog allowlist in Discover.
	return `lower((` + expression + `)::text COLLATE pg_catalog."` + b.collation + `")`
}

func discoveryTerm(value, label string, identifier bool) string {
	return `SELECT ` + value + `::text AS value,` + fmt.Sprint(identifier) + ` AS identifier,` + label + `::text AS label`
}

func (b *discoverySQL) source(typ, company string) string {
	table, title, description, identifier := "", "''", "''", "l.id::text"
	var fields []string
	hasFields := false
	status, joins := "''::text", ""
	url := "'/" + typ + "s/'||l.id::text"
	switch typ {
	case "customer":
		table, title, description = "customers", "l.display_name", "l.service_notes"
		fields = []string{"display_name", "first_name", "last_name", "company_name", "email", "phone", "notes", "service_notes"}
		hasFields = true
	case "contact":
		table, title, description = "customer_contacts", "trim(l.first_name||' '||l.last_name)", "l.role_title"
		fields = []string{"first_name", "last_name", "email", "phone", "role_title", "notes", "service_notes"}
	case "location":
		table, title, description = "locations", "l.title", "concat_ws(' ',l.address_1,l.address_2,l.city,l.state,l.zip_code)"
		fields = []string{"title", "address_1", "address_2", "city", "state", "zip_code", "notes", "service_notes", "access_notes"}
	case "asset":
		table, title, description, identifier = "assets", "l.name", "l.model", "l.number"
		fields = []string{"name", "serial_number", "model", "manufacturer", "notes"}
		hasFields = true
	case "job":
		table, title, description = "jobs", "l.job_type", "l.subtitle"
		fields = []string{"job_type", "subtitle", "notes"}
		hasFields = true
	case "project":
		table, title, description = "projects", "l.name", "l.description"
		fields = []string{"name", "description", "notes", "service_notes"}
		hasFields = true
	case "estimate", "invoice":
		table, title, description, identifier = typ+"s", "l.title", "l.notes", "l.number_label"
		fields = []string{"title", "notes", "terms"}
		hasFields = true
	case "item":
		table, title, description = "items", "l.name", "l.description"
		fields = []string{"name", "description"}
	case "lease":
		table, title, identifier = "leases", "l.title", "l.number_label"
		fields = []string{"title", "reference"}
		hasFields = b.p.Allows("lease.view", All)
		status = leaseWorkspaceLifecycle
		if !hasFields {
			title, fields = "''", nil
			url = "'/leases/'||l.id::text||'/billing'"
		}
	}
	if typ == "job" || typ == "project" || typ == "estimate" {
		joins = " LEFT JOIN statuses ds ON ds.id=l.status_id AND ds.company_id=l.company_id"
		status = "coalesce(ds.name,'')"
	} else if typ == "invoice" {
		joins = " LEFT JOIN invoice_effective_status es ON es.invoice_id=l.id LEFT JOIN statuses ds ON ds.id=es.status_id AND ds.company_id=l.company_id"
		status = "coalesce(ds.name,'')"
	}
	label := "'" + strings.ToUpper(typ[:1]) + typ[1:] + " match'"
	terms := []string{discoveryTerm("l.id::text", label, true)}
	if identifier != "l.id::text" {
		terms = append(terms, discoveryTerm(identifier, label, true))
	}
	// The full display name and full address are also individual public values.
	terms = append(terms, discoveryTerm(title, label, false), discoveryTerm(description, label, false))
	for _, field := range fields {
		terms = append(terms, discoveryTerm("l."+field, label, false))
	}
	if typ == "item" && b.p.Allows("pricebook.view", All) {
		terms = append(terms, discoveryTerm("l.sku", "'Item SKU match'", true))
	}
	terms = append(terms, b.scalarTerms(typ, status)...)
	if hasFields {
		terms = append(terms, discoveryCustomTerms(typ, company))
	}
	if typ == "customer" {
		terms = append(terms, b.customerTerms()...)
	}
	if typ == "lease" && b.p.Allows("lease.view", All) {
		terms = append(terms, b.leaseTerms()...)
	}
	where := "l.company_id=" + company
	if typ == "item" {
		where = "(l.company_id=" + company + " OR l.company_id IS NULL)"
	}
	if typ == "location" {
		where += " AND l.object_type='customer'"
	}
	switch b.q.State {
	case "active":
		where += " AND l.deleted_at IS NULL"
	case "archived":
		where += " AND l.deleted_at IS NOT NULL"
	}
	if typ == "estimate" || typ == "invoice" {
		where += " AND l.conversion_hidden_at IS NULL"
	}
	if (typ == "job" || typ == "estimate" || typ == "invoice") && !b.p.Allows(typ+".view", All) {
		anchor := "l.job_id"
		if typ == "job" {
			anchor = "l.id"
		}
		where += " AND EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=" + anchor + " AND j.company_id=" + company + " AND j.deleted_at IS NULL AND a.user_id=" + b.bind(b.p.UserID) + ")"
	}
	filter := "true"
	if typ == "lease" {
		filter += b.q.Lease.where(b.bind)
		for _, queue := range leaseWorkspaceQueues {
			if b.q.Lease.Queue == queue.Key {
				filter += " AND (" + queue.predicate + ")"
			}
		}
	}
	return `SELECT '` + typ + `'::text typ,l.id,coalesce(` + identifier + `,'') identifier,coalesce(` + title + `,'') title,
 coalesce(` + description + `,'') description,` + url + ` url,l.deleted_at IS NOT NULL archived,` + status + ` status,
 (` + filter + `) filter_ok,(SELECT jsonb_agg(to_jsonb(t)) FROM (` + strings.Join(terms, " UNION ALL ") + `) t WHERE t.value IS NOT NULL) terms
 FROM ` + table + ` l` + joins + ` WHERE ` + where
}

// scalarTerms is the built-in scalar manifest. It intentionally enumerates
// actual presentation fields rather than indexing row JSON, technical foreign
// keys, revisions, legacy CRM state, histories or derived financial aggregates.
// Dates use ISO YYYY-MM-DD; instants use UTC RFC3339. Stored money uses two
// decimal dollars; percentages retain their stored decimal spelling.
func (b *discoverySQL) scalarTerms(typ, status string) []string {
	var out []string
	add := func(expr string) {
		out = append(out, discoveryTerm(expr, "'"+strings.ToUpper(typ[:1])+typ[1:]+" field match'", false))
	}
	date := func(column string) { add("to_char(" + column + ",'YYYY-MM-DD')") }
	instant := func(column string) { add(`to_char(` + column + ` AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`) }
	switch typ {
	case "customer":
		add("l.account_type")
		if b.p.Allows("customer.financial.view", All) {
			for _, field := range []string{"billing_address_1", "billing_address_2", "billing_city", "billing_state", "billing_zip_code"} {
				add("l." + field)
			}
		}
	case "asset":
		add("l.ownership_class")
		date("l.installed_at")
		date("l.warranty_expires")
		add("(SELECT name FROM asset_types x WHERE x.id=l.asset_type_id AND (x.company_id=l.company_id OR x.company_id IS NULL))")
		add("(SELECT name FROM asset_statuses x WHERE x.id=l.asset_status_id AND (x.company_id=l.company_id OR x.company_id IS NULL))")
	case "job":
		add(status)
		instant("l.start_time")
		instant("l.end_time")
		date("l.due_date")
		instant("l.arrival_window_start")
		instant("l.arrival_window_end")
		add("l.tech_notes")
		out = append(out, discoveryTerm("l.billing_type", "'Job pricing match'", false)+" WHERE "+b.pricingAllowed("l.id"))
	case "project":
		add(status)
		add("l.completion_percentage::text")
		date("l.planned_start_date")
		date("l.planned_end_date")
	case "estimate":
		add(status)
		date("l.valid_through")
		out = append(out, discoveryTerm("l.tax_rate", "'Estimate pricing match'", false)+" WHERE "+b.pricingAllowed("l.job_id"))
	case "invoice":
		add(status)
		date("l.invoice_date")
		date("nullif(l.due_date,'0001-01-01'::date)")
		out = append(out, discoveryTerm("l.tax_rate", "'Invoice pricing match'", false)+" WHERE "+b.pricingAllowed("l.job_id"))
	case "item":
		add("l.type")
		if b.p.Allows("pricebook.view", All) {
			add("round(l.unit_price::numeric,2)::text")
			add("l.tax_rate")
		}
		if b.p.Allows("cost.view", All) {
			add("round(l.unit_cost::numeric,2)::text")
		}
	case "lease":
		add(status)
		if b.p.Allows("lease.view", All) {
			date("l.execution_date")
			date("l.starts_on")
			date("l.ends_on")
		}
		if b.p.Allows("lease.billing.view", All) {
			out = append(out, `SELECT to_char(x.initial_due_on,'YYYY-MM-DD'),false,'Lease Billing due date match' FROM lease_draft_billing x WHERE x.lease_id=l.id`)
			out = append(out, discoveryLeaseBillingTerms)
		}
	}
	return out
}

func (b *discoverySQL) pricingAllowed(anchor string) string {
	if b.p.Allows("pricing.view", All) {
		return "true"
	}
	if !b.p.Allows("pricing.view", Assigned) {
		return "false"
	}
	return "EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=" + anchor +
		" AND j.company_id=l.company_id AND j.deleted_at IS NULL AND a.user_id=" + b.bind(b.p.UserID) + ")"
}

func discoveryCustomTerms(typ, company string) string {
	// Options are stored as stable labels, not ordinal indexes. Array expansion
	// is only for multiselect; a definition/type-aware join excludes unknown,
	// retired, foreign and non-scalar values before matching.
	return `SELECT scalar.value,false,'Custom field match: '||d.name
 FROM jsonb_array_elements(l.custom_fields::jsonb) v
 JOIN custom_field_definitions d ON d.id::text=v->>'definition_id'
 AND d.object_type='` + typ + `' AND custom_field_applies_to_company(d.company_id,` + company + `)
 AND NOT d.retired AND d.field_type IN ('text','textarea','number','date','select','multiselect')
 CROSS JOIN LATERAL (
 SELECT v->>'value' value WHERE d.field_type<>'multiselect' AND jsonb_typeof(v->'value') IN ('string','number')
 UNION ALL
 SELECT x #>> '{}' FROM jsonb_array_elements(CASE WHEN d.field_type='multiselect' AND jsonb_typeof(v->'value')='array' THEN v->'value' ELSE '[]'::jsonb END) x WHERE jsonb_typeof(x)='string'
 ) scalar
 WHERE d.field_type NOT IN ('select','multiselect') OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(d.options::jsonb) o WHERE o=scalar.value)`
}

func (b *discoverySQL) customerTerms() []string {
	var terms []string
	if b.p.Allows("contact.view", All) {
		terms = append(terms, `SELECT v.value,false,'Contact match: '||trim(x.first_name||' '||x.last_name)||' (#'||x.id::text||')'
 FROM customer_contacts x CROSS JOIN LATERAL unnest(ARRAY[trim(x.first_name||' '||x.last_name),x.first_name,x.last_name,x.email,x.phone]) v(value)
 WHERE x.company_id=l.company_id AND x.customer_id=l.id AND x.deleted_at IS NULL`)
	}
	if b.p.Allows("location.view", All) {
		terms = append(terms, `SELECT v.value,false,'Location match: '||x.title||' (#'||x.id::text||')'
 FROM locations x CROSS JOIN LATERAL unnest(ARRAY[x.title,x.address_1,x.address_2,x.city,x.state,x.zip_code,concat_ws(' ',x.address_1,x.address_2,x.city,x.state,x.zip_code)]) v(value)
 WHERE x.company_id=l.company_id AND x.object_type='customer' AND x.object_id=l.id AND x.deleted_at IS NULL`)
	}
	return terms
}

func (b *discoverySQL) leaseTerms() []string {
	var terms []string
	if b.p.Allows("customer.view", All) {
		terms = append(terms, `SELECT x.display_name,false,'Customer match: '||x.display_name FROM customers x
 WHERE x.id=l.customer_id AND x.company_id=l.company_id AND (x.deleted_at IS NULL OR `+b.bind(b.p.Allows("customer.archived.view", All))+`)`)
	}
	if b.p.Allows("location.view", All) {
		terms = append(terms, `SELECT v.value,false,'Premises match: '||x.title FROM locations x
 CROSS JOIN LATERAL unnest(ARRAY[x.title,x.address_1,x.address_2,x.city,x.state,x.zip_code,concat_ws(' ',x.address_1,x.address_2,x.city,x.state,x.zip_code)]) v(value)
 WHERE x.id=l.premises_id AND x.company_id=l.company_id AND (x.deleted_at IS NULL OR `+b.bind(b.p.Allows("location.archived.view", All))+`)`)
	}
	if b.p.Allows("asset.view", All) {
		terms = append(terms, `SELECT x.number,false,'Asset match: '||x.number FROM assets x
 WHERE x.company_id=l.company_id AND (x.deleted_at IS NULL OR `+b.bind(b.p.Allows("asset.archived.view", All))+`)
 AND (EXISTS(SELECT 1 FROM lease_intended_assets i WHERE i.lease_id=l.id AND i.asset_id=x.id)
 OR EXISTS(SELECT 1 FROM lease_asset_dependencies d WHERE d.lease_id=l.id AND d.asset_id=x.id))`)
	}
	return terms
}

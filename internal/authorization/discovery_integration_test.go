package authorization_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

func discoveryActor(t *testing.T, f commercialReportFixture, grants ...authorization.Grant) authorization.Actor {
	t.Helper()
	role, err := f.svc.SaveRole(context.Background(), f.actor, 0, fmt.Sprintf("Discovery reader %d", time.Now().UnixNano()), grants)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = f.pool.QueryRow(context.Background(), `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,$2,'Discovery reader','hash','admin',$3) RETURNING id`, f.company, fmt.Sprintf("discovery-%d@test", role), role).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return authorization.Actor{UserID: id, Channel: "web"}
}

func discover(t *testing.T, f commercialReportFixture, a authorization.Actor, q authorization.DiscoveryQuery) authorization.DiscoveryPage {
	t.Helper()
	p, err := f.svc.Discover(context.Background(), a, q)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscoveryTenTypesAndLiteralGlobalPages(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	initial := discover(t, f.commercialReportFixture, f.actor, authorization.DiscoveryQuery{}).Total
	for _, statement := range []string{
		`INSERT INTO customer_contacts(company_id,customer_id,first_name) VALUES($1,$2,'Contact discovery')`,
		`INSERT INTO projects(company_id,customer_id,name) VALUES($1,$2,'Project discovery')`,
		`INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'Estimate discovery')`,
		`INSERT INTO invoices(company_id,customer_id,title,invoice_date) VALUES($1,$2,'Invoice discovery',CURRENT_DATE)`,
	} {
		if _, err := f.pool.Exec(ctx, statement, f.company, f.customer); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO items(company_id,name,description,sku) VALUES($1,'Literal %_ ÉQUIPMENT','duplicate','unique-sku')`, f.company); err != nil {
		t.Fatal(err)
	}
	p := discover(t, f.commercialReportFixture, f.actor, authorization.DiscoveryQuery{})
	seen := map[string]bool{}
	for _, row := range p.Results {
		seen[row.Type] = true
		if row.URL == "" {
			t.Fatal("missing destination")
		}
	}
	if len(seen) != 10 || p.Total != initial+5 || !p.HasVisibleRecords {
		t.Fatalf("ten catalogs: %+v", p)
	}
	for _, text := range []string{"%_", "équipment", "UNIQUE-SKU"} {
		p = discover(t, f.commercialReportFixture, f.actor, authorization.DiscoveryQuery{Search: text})
		if p.Total != 1 || p.Results[0].Type != "item" {
			t.Fatalf("literal case-insensitive %q: %+v", text, p)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO items(company_id,name) SELECT $1,'page-tie' FROM generate_series(1,111)`, f.company); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{25, 50, 100} {
		var ids []int64
		for page := 1; page <= (111+size-1)/size; page++ {
			p = discover(t, f.commercialReportFixture, f.actor, authorization.DiscoveryQuery{Search: "page-tie", Page: page, PerPage: size})
			if p.Total != 111 {
				t.Fatal(p.Total)
			}
			for _, row := range p.Results {
				if len(ids) > 0 && row.ID <= ids[len(ids)-1] {
					t.Fatal("unstable or duplicate tied page")
				}
				ids = append(ids, row.ID)
			}
		}
		if len(ids) != 111 {
			t.Fatal(len(ids))
		}
	}
	p = discover(t, f.commercialReportFixture, f.actor, authorization.DiscoveryQuery{Search: "page-tie", Page: 100})
	if p.Total != 111 || len(p.Results) != 0 {
		t.Fatalf("empty page lost count: %+v", p)
	}
}

func TestDiscoveryIndependentFacetsRelationshipsAndCurrentAuthority(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO items(company_id,name,sku,unit_price,unit_cost,tax_rate) VALUES($1,'Visible item','facet-secret',987.65,876.54,'19.321');
 `, f.company); err != nil {
		t.Fatal(err)
	}
	var contact int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name,email) VALUES($1,$2,'Related person','relationship-secret') RETURNING id`, f.company, f.customer).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	base := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "item.view", Scope: authorization.All}, authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	for _, term := range []string{"facet-secret", "relationship-secret", "987.65", "876.54", "19.321"} {
		p := discover(t, f.commercialReportFixture, base, authorization.DiscoveryQuery{Search: term})
		if p.Total != 0 {
			t.Fatalf("hidden facet matched: %+v", p)
		}
	}
	granted := discoveryActor(t, f.commercialReportFixture,
		authorization.Grant{Permission: "item.view", Scope: authorization.All},
		authorization.Grant{Permission: "pricebook.view", Scope: authorization.All},
		authorization.Grant{Permission: "customer.view", Scope: authorization.All},
		authorization.Grant{Permission: "contact.view", Scope: authorization.All})
	p := discover(t, f.commercialReportFixture, granted, authorization.DiscoveryQuery{Search: "facet-secret"})
	if p.Total != 1 || p.Results[0].Type != "item" {
		t.Fatalf("SKU: %+v", p)
	}
	for _, term := range []string{"987.65", "19.321"} {
		if p := discover(t, f.commercialReportFixture, granted, authorization.DiscoveryQuery{Search: term}); p.Total != 1 {
			t.Fatalf("Pricebook scalar %q: %+v", term, p)
		}
	}
	if p := discover(t, f.commercialReportFixture, granted, authorization.DiscoveryQuery{Search: "876.54"}); p.Total != 0 {
		t.Fatal("Pricebook leaked Cost")
	}
	cost := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "item.view", Scope: authorization.All}, authorization.Grant{Permission: "cost.view", Scope: authorization.All})
	if p := discover(t, f.commercialReportFixture, cost, authorization.DiscoveryQuery{Search: "876.54"}); p.Total != 1 {
		t.Fatalf("Cost scalar missing: %+v", p)
	}
	p = discover(t, f.commercialReportFixture, granted, authorization.DiscoveryQuery{Search: "relationship-secret", Types: []string{"customer"}})
	if p.Total != 1 || !strings.Contains(p.Results[0].Match, "Contact match: Related person (#"+strconv.FormatInt(contact, 10)+")") {
		t.Fatalf("relationship provenance: %+v", p)
	}
	billing := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	p = discover(t, f.commercialReportFixture, billing, authorization.DiscoveryQuery{})
	if p.Total != 1 || p.Results[0].Title != "" || p.Results[0].Description != "" || !strings.HasSuffix(p.Results[0].URL, "/billing") {
		t.Fatalf("Billing-only projection: %+v", p)
	}
	p = discover(t, f.commercialReportFixture, billing, authorization.DiscoveryQuery{Search: "Projection Lease"})
	if p.Total != 0 {
		t.Fatal("Billing-only searched base title")
	}
	p = discover(t, f.commercialReportFixture, billing, authorization.DiscoveryQuery{Search: "123.45"})
	if p.Total != 1 {
		t.Fatalf("Billing rent scalar missing: %+v", p)
	}
	leaseBase := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	p = discover(t, f.commercialReportFixture, leaseBase, authorization.DiscoveryQuery{Search: "123.45"})
	if p.Total != 0 {
		t.Fatal("base Lease searched Billing rent")
	}
	_, err := f.svc.Discover(ctx, billing, authorization.DiscoveryQuery{Types: []string{"lease"}, Lease: authorization.LeaseWorkspaceQuery{Customer: f.customer}})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("Billing-only relation filter: %v", err)
	}
	jobReader := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "job.view", Scope: authorization.Assigned})
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, jobReader.UserID); err != nil {
		t.Fatal(err)
	}
	p = discover(t, f.commercialReportFixture, jobReader, authorization.DiscoveryQuery{})
	if p.Total != 1 || p.Results[0].ID != f.job {
		t.Fatalf("assignment scope: %+v", p)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, jobReader.UserID); err != nil {
		t.Fatal(err)
	}
	p = discover(t, f.commercialReportFixture, jobReader, authorization.DiscoveryQuery{})
	if p.Total != 0 || p.HasVisibleRecords {
		t.Fatal("stale assignment or hidden existence")
	}
}

func TestDiscoveryWaiterReauthorizesAfterRevocation(t *testing.T) {
	f := newCommercialReportFixture(t)
	a := discoveryActor(t, f, authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config := f.pool.Config()
	config.MaxConns = 1
	waiterPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterPool.Close()
	var waiter int
	if err := waiterPool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&waiter); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		page, err := authorization.New(waiterPool).Discover(ctx, a, authorization.DiscoveryQuery{})
		if len(page.Results) != 0 {
			result <- fmt.Errorf("revoked actor received results: %+v", page)
			return
		}
		result <- err
	}()
	// Observe an actual blocked database waiter rather than assuming an
	// interleaving from a sleep. The connection holding the fence can perform
	// the account change and commit before discovery loads its principal.
	for {
		var waiting bool
		if err := tx.QueryRow(ctx, `SELECT $1=ANY(pg_blocking_pids($2))`, blocker, waiter).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, a.UserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("waiting read did not reauthorize: %v", err)
	}
}

func TestDiscoveryActiveScalarFieldsRankArchiveAndFailure(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	var values []map[string]any
	for i, tc := range []struct {
		kind  string
		value any
	}{{"text", "scalar-text"}, {"textarea", "scalar\nmultiline"}, {"number", "-12.340"}, {"date", "2026-11-27"}, {"select", "Option Label"}, {"multiselect", []string{"Second Label"}}} {
		var id int64
		if err := f.pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,options) VALUES($1,'customer',$2,$3,3,'["Option Label","Second Label"]') RETURNING id`, f.company, fmt.Sprintf("Discovery field %d", i), tc.kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		values = append(values, map[string]any{"definition_id": id, "value": tc.value})
	}
	raw, _ := json.Marshal(values)
	if _, err := f.pool.Exec(ctx, `UPDATE customers SET custom_fields=$1 WHERE id=$2`, raw, f.customer); err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"scalar-text", "multiline", "-12.340", "2026-11-27", "option label", "second label"} {
		p := discover(t, f, f.actor, authorization.DiscoveryQuery{Search: term, Types: []string{"customer"}})
		if p.Total != 1 || !strings.HasPrefix(p.Results[0].Match, "Custom field match:") {
			t.Fatalf("scalar %q: %+v", term, p)
		}
	}
	if _, err := f.pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true WHERE company_id=$1 AND object_type='customer'`, f.company); err != nil {
		t.Fatal(err)
	}
	if p := discover(t, f, f.actor, authorization.DiscoveryQuery{Search: "scalar-text"}); p.Total != 0 {
		t.Fatal("retired field matched")
	}
	var exact int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO items(company_id,name,sku) VALUES($1,'Elsewhere token','token') RETURNING id`, f.company).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO items(company_id,name) VALUES($1,'token prefix'),($1,'elsewhere token')`, f.company); err != nil {
		t.Fatal(err)
	}
	p := discover(t, f, f.actor, authorization.DiscoveryQuery{Search: "token"})
	if p.Total != 3 || p.Results[0].ID != exact || p.Results[1].Title != "token prefix" {
		t.Fatalf("ranking: %+v", p)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE items SET deleted_at=now() WHERE id=$1`, exact); err != nil {
		t.Fatal(err)
	}
	p = discover(t, f, f.actor, authorization.DiscoveryQuery{Search: "token", Types: []string{"item"}, State: "archived"})
	if p.Total != 1 || !p.Results[0].Archived {
		t.Fatal("archived opt-in missing badge")
	}
	// A dependency failure in one selected branch must not expose successful
	// branches as a new incomplete page. Restore the table for fixture cleanup.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE items RENAME TO discovery_unavailable_items`); err != nil {
		t.Fatal(err)
	}
	defer f.pool.Exec(ctx, `ALTER TABLE discovery_unavailable_items RENAME TO items`)
	failed, err := f.svc.Discover(ctx, f.actor, authorization.DiscoveryQuery{})
	if err == nil || !reflect.DeepEqual(failed, authorization.DiscoveryPage{}) {
		t.Fatalf("partial result after unavailable branch: %+v %v", failed, err)
	}
}

func TestDiscoveryRejectsInvalidOrUnavailableFilters(t *testing.T) {
	f := newCommercialReportFixture(t)
	for _, q := range []authorization.DiscoveryQuery{
		{Types: []string{"time"}}, {Types: []string{"item", "item"}}, {Sort: "id;drop"},
		{Page: -1}, {PerPage: 10}, {State: "garbage"}, {Search: strings.Repeat("x", 501)},
		{Lease: authorization.LeaseWorkspaceQuery{Lifecycle: "active"}},
	} {
		if _, err := f.svc.Discover(context.Background(), f.actor, q); !errors.Is(err, authorization.ErrConflict) {
			t.Fatalf("invalid %+v: %v", q, err)
		}
	}
	reader := discoveryActor(t, f, authorization.Grant{Permission: "item.view", Scope: authorization.All})
	for _, q := range []authorization.DiscoveryQuery{{Types: []string{"customer"}}, {State: "all"}} {
		if _, err := f.svc.Discover(context.Background(), reader, q); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("unauthorized %+v: %v", q, err)
		}
	}
	reader.Channel = "api"
	if _, err := f.svc.Discover(context.Background(), reader, authorization.DiscoveryQuery{}); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("API discovery: %v", err)
	}
}

func TestDiscoveryHiddenInputsCannotChangeVisibleRankOrExistence(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	reader := discoveryActor(t, f, authorization.Grant{Permission: "item.view", Scope: authorization.All})
	var item int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO items(company_id,name) VALUES($1,'elsewhere rank-secret') RETURNING id`, f.company).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO items(company_id,name) VALUES($1,'rank-secret prefix')`, f.company); err != nil {
		t.Fatal(err)
	}
	q := authorization.DiscoveryQuery{Search: "rank-secret"}
	before := discover(t, f, reader, q)
	if _, err := f.pool.Exec(ctx, `UPDATE items SET sku='rank-secret',unit_price=9123.45,unit_cost=8123.45 WHERE id=$1`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'rank-secret')`, f.company); err != nil {
		t.Fatal(err)
	}
	var foreign int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other discovery company','other-discovery-company') RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO items(company_id,name,sku) VALUES($1,'rank-secret','foreign-rank-secret')`, foreign); err != nil {
		t.Fatal(err)
	}
	after := discover(t, f, reader, q)
	if before.Total != after.Total || before.HasVisibleRecords != after.HasVisibleRecords || !reflect.DeepEqual(before.Results, after.Results) {
		t.Fatalf("hidden inputs changed visible ranking: before=%+v after=%+v", before, after)
	}
	for _, typ := range []string{"estimate", "invoice"} {
		var number string
		if err := f.pool.QueryRow(ctx, `INSERT INTO `+typ+`s(company_id,customer_id,title,conversion_hidden_at) VALUES($1,$2,'hidden-provenance-secret',now()) RETURNING number_label`, f.company, f.customer).Scan(&number); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"hidden-provenance-secret", number} {
			for _, state := range []string{"active", "archived", "all"} {
				p := discover(t, f, f.actor, authorization.DiscoveryQuery{Search: text, Types: []string{typ}, State: state})
				if p.Total != 0 || p.HasVisibleRecords {
					t.Fatalf("tombstone affected discovery: %+v", p)
				}
			}
		}
	}
	contextOnly := discoveryActor(t, f, authorization.Grant{Permission: "customer.view", Scope: authorization.Context}, authorization.Grant{Permission: "job.view", Scope: authorization.Assigned})
	if _, err := f.svc.Discover(ctx, contextOnly, authorization.DiscoveryQuery{Types: []string{"customer"}}); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("Context-only master discovery: %v", err)
	}
	if p := discover(t, f, contextOnly, authorization.DiscoveryQuery{}); p.Total != 0 || p.HasVisibleRecords {
		t.Fatalf("Context without assigned Jobs discovered masters: %+v", p)
	}
}

func TestDiscoveryLeaseFiltersAndIndependentRelationVisibility(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	filters := []authorization.LeaseWorkspaceQuery{
		{Lifecycle: "active"}, {Customer: f.customer}, {Asset: f.asset},
		{Premises: f.premises}, {Placement: "unplaced"}, {Queue: "monthly"},
		{EndingFrom: f.today.Format(time.DateOnly), EndingThrough: f.today.AddDate(2, 0, 0).Format(time.DateOnly)},
		{Lifecycle: "active", Customer: f.customer, Asset: f.asset, Premises: f.premises, Placement: "unplaced"},
	}
	for _, filter := range filters {
		p := discover(t, f.commercialReportFixture, f.actor, authorization.DiscoveryQuery{Types: []string{"lease"}, Lease: filter})
		if p.Total != 1 || p.Results[0].ID != f.lease {
			t.Fatalf("Lease filter %+v: %+v", filter, p)
		}
	}
	base := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	for _, filter := range []authorization.LeaseWorkspaceQuery{{Billing: "failed"}, {Queue: "monthly"}, {Asset: f.asset}, {Premises: f.premises}, {Customer: f.customer}} {
		if _, err := f.svc.Discover(ctx, base, authorization.DiscoveryQuery{Types: []string{"lease"}, Lease: filter}); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("Lease-only forbidden filter %+v: %v", filter, err)
		}
	}
	for _, filter := range []authorization.LeaseWorkspaceQuery{{Lifecycle: "invented"}, {EndingFrom: "2026-99-99"}, {EndingFrom: "2026-12-01", EndingThrough: "2026-11-01"}, {Customer: -1}} {
		if _, err := f.svc.Discover(ctx, f.actor, authorization.DiscoveryQuery{Types: []string{"lease"}, Lease: filter}); !errors.Is(err, authorization.ErrConflict) {
			t.Fatalf("malformed Lease filter %+v: %v", filter, err)
		}
	}
	if _, err := f.pool.Exec(ctx, `UPDATE customers SET display_name='independent-customer-secret' WHERE id=$1`, f.customer); err != nil {
		t.Fatal(err)
	}
	p := discover(t, f.commercialReportFixture, base, authorization.DiscoveryQuery{Search: "independent-customer-secret"})
	if p.Total != 0 {
		t.Fatal("Lease base leaked related Customer name")
	}
}

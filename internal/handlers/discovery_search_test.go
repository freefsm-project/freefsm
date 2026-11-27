package handlers

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestDiscoveryTenTypesProductionHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Discovery106 customer").SaveX(ctx)
	contact := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName("Discovery106 contact").SaveX(ctx)
	location := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Discovery106 location").SaveX(ctx)
	typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Equipment106").SaveX(ctx)
	asset := f.client.Asset.Create().SetCompanyID(f.company).SetOwnershipClass("customer_owned").SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Discovery106 asset").SaveX(ctx)
	job := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Discovery106 job").SaveX(ctx)
	project := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetName("Discovery106 project").SaveX(ctx)
	estimate := f.client.Estimate.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetTitle("Discovery106 estimate").SaveX(ctx)
	invoice := f.client.Invoice.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetTitle("Discovery106 invoice").SetInvoiceNumber(1).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	item := f.client.Item.Create().SetCompanyID(f.company).SetName("Discovery106 item").SetDescription("Discovery106 repeated").SetSku("Discovery106-SKU").SaveX(ctx)
	svc := authorization.New(f.pool)
	lease, err := svc.RegisterDraftLease(ctx, authorization.Actor{UserID: f.admin}, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, authorization.Actor{UserID: f.admin}, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Discovery106 lease"}))
	want := []string{
		fmt.Sprintf("asset:%d", asset.ID), fmt.Sprintf("contact:%d", contact.ID), fmt.Sprintf("customer:%d", c.ID),
		fmt.Sprintf("estimate:%d", estimate.ID), fmt.Sprintf("invoice:%d", invoice.ID), fmt.Sprintf("item:%d", item.ID),
		fmt.Sprintf("job:%d", job.ID), fmt.Sprintf("lease:%d", lease.ID), fmt.Sprintf("location:%d", location.ID), fmt.Sprintf("project:%d", project.ID),
	}
	got := f.search(t, f.cookie, "q=dIsCoVeRy106")
	discoveryEqual(t, got.IDs, want)
	discoveryEqual(t, got.Total, 10)
	for _, typ := range []string{"asset", "contact", "customer", "estimate", "invoice", "item", "job", "lease", "location", "project"} {
		o := f.search(t, f.cookie, "q=Discovery106&type="+typ)
		discoveryEqual(t, len(o.IDs), 1)
		discoveryEqual(t, o.Total, 1)
	}
	_, _, leaseOnly := f.reader(t, "LeaseOnly", authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	discoveryEqual(t, f.search(t, leaseOnly, "q=Discovery106").IDs, []string{fmt.Sprintf("lease:%d", lease.ID)})
	_, _, empty := f.reader(t, "Empty")
	requestBody(t, f.web, empty, "GET", "/search?q=Discovery106", 403)
}

func TestDiscoveryFacetAndTenantNoninterferenceHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	_, role, cookie := f.reader(t, "Base", authorization.Grant{Permission: "item.view", Scope: authorization.All})
	empty := f.search(t, cookie, "q=hiddenonly106")
	f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("hiddenonly106").SaveX(ctx)
	discoveryEqual(t, f.search(t, cookie, "q=hiddenonly106"), empty)
	first := f.client.Item.Create().SetCompanyID(f.company).SetName("Earlier needlerank").SaveX(ctx)
	second := f.client.Item.Create().SetCompanyID(f.company).SetName("Later needlerank").SetSku("needlerank").SetUnitCost(987.65).SaveX(ctx)
	before := f.search(t, cookie, "q=needlerank")
	discoveryEqual(t, before.IDs, []string{fmt.Sprintf("item:%d", first.ID), fmt.Sprintf("item:%d", second.ID)})
	var foreign int64
	historyCheck(t, f.pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Foreign106','foreign106') RETURNING id`).Scan(&foreign))
	f.client.Item.Create().SetCompanyID(foreign).SetName("needlerank").SetSku("FOREIGN106").SaveX(ctx)
	for range 110 {
		f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("needlerank hidden").SaveX(ctx)
	}
	discoveryEqual(t, f.search(t, cookie, "q=needlerank"), before)
	noMatch := f.search(t, cookie, "q=secret106")
	f.client.Item.UpdateOneID(second.ID).SetSku("secret106").SaveX(ctx)
	discoveryEqual(t, f.search(t, cookie, "q=secret106"), noMatch)
	f.client.Item.UpdateOneID(second.ID).SetSku("needlerank").SaveX(ctx)
	_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "RenamedBase", []authorization.Grant{
		{Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All},
	}, 1)
	historyCheck(t, err)
	discoveryEqual(t, f.search(t, cookie, "q=needlerank").IDs, []string{fmt.Sprintf("item:%d", second.ID), fmt.Sprintf("item:%d", first.ID)})
	requestBody(t, f.web, cookie, "GET", "/search?q=needlerank&type=customer", 403)
}

func TestDiscoveryGlobalRankAndPaginationHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	var prefix, elsewhere []string
	for i := 0; i < 137; i++ {
		name := "Page106 repeated"
		if i%3 == 0 {
			name = "Elsewhere Page106 repeated"
		}
		var key string
		if i%2 == 0 {
			r := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName(name).SaveX(ctx)
			key = fmt.Sprintf("customer:%020d", r.ID)
		} else {
			r := f.client.Item.Create().SetCompanyID(f.company).SetName(name).SaveX(ctx)
			key = fmt.Sprintf("item:%020d", r.ID)
		}
		if i%3 == 0 {
			elsewhere = append(elsewhere, key)
		} else {
			prefix = append(prefix, key)
		}
	}
	exact := f.client.Item.Create().SetCompanyID(f.company).SetName("Exact identifier").SetSku("Page106").SaveX(ctx)
	sort.Strings(prefix)
	sort.Strings(elsewhere)
	want := []string{fmt.Sprintf("item:%d", exact.ID)}
	for _, key := range append(prefix, elsewhere...) {
		parts := strings.Split(key, ":")
		want = append(want, parts[0]+":"+strings.TrimLeft(parts[1], "0"))
	}
	for _, size := range []int{25, 50, 100} {
		var ids []string
		for page := 1; page <= (len(want)+size-1)/size; page++ {
			q := fmt.Sprintf("q=Page106&per_page=%d&page=%d", size, page)
			a := f.search(t, f.cookie, q)
			discoveryEqual(t, a.Total, len(want))
			discoveryEqual(t, f.search(t, f.cookie, q), a)
			ids = append(ids, a.IDs...)
		}
		discoveryEqual(t, ids, want)
	}
	discoveryEqual(t, len(f.search(t, f.cookie, "q=Page106").IDs), 25)
	out := f.search(t, f.cookie, "q=Page106&page=100")
	discoveryEqual(t, out.Total, len(want))
	discoveryEqual(t, len(out.IDs), 0)
}

func TestDiscoveryCustomerRelationshipAuthorityHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Neutral owner").SaveX(ctx)
	child := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName("A Unique relationship").SetEmail("relation106@example.test").SaveX(ctx)
	f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName("Z Second match").SetEmail("relation106@example.test").SaveX(ctx)
	_, role, cookie := f.reader(t, "Relationships", authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	discoveryEqual(t, f.search(t, cookie, "q=relation106").Total, 0)
	_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Relationships", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}}, 1)
	historyCheck(t, err)
	o := f.search(t, cookie, "q=relation106&type=customer")
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("customer:%d", c.ID)})
	if !strings.Contains(strings.Join(o.Text, " "), "Unique relationship") {
		t.Fatalf("missing authorized relationship label: %+v", o)
	}
	masterPost(t, f.web, f.cookie, fmt.Sprintf("/customers/%d/contacts/%d/delete", c.ID, child.ID), nil, 303)
	o = f.search(t, cookie, "q=relation106&type=customer")
	if strings.Contains(strings.Join(o.Text, " "), "Unique relationship") {
		t.Fatal("archived child influenced match")
	}
}

func TestDiscoveryLiteralAndRetiredScalarHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	for _, tc := range []struct{ kind, value string }{{"text", "Scalar106 text"}, {"textarea", "Scalar106 multiline"}, {"number", "-106.25"}, {"date", "2026-06-10"}, {"select", "Scalar106 option"}, {"multiselect", "Scalar106 option"}} {
		t.Run(tc.kind, func(t *testing.T) {
			def := url.Values{"name": {"Search " + tc.kind}, "object_type": {"customer"}, "field_type": {tc.kind}}
			if tc.kind == "number" {
				def.Set("precision", "2")
			}
			if tc.kind == "select" || tc.kind == "multiselect" {
				def.Set("options", `["Scalar106 option","Other"]`)
			}
			fieldPost(t, f.web, f.cookie, "/settings/custom-fields", def, 303)
			var id int64
			historyCheck(t, f.pool.QueryRow(ctx, `SELECT id FROM custom_field_definitions WHERE company_id=$1 AND name=$2`, f.company, "Search "+tc.kind).Scan(&id))
			values := url.Values{"display_name": {"Scalar owner " + tc.kind}, fmt.Sprintf("custom_field_%d", id): {tc.value}}
			if tc.kind == "multiselect" {
				values.Del(fmt.Sprintf("custom_field_%d", id))
				values.Set(fmt.Sprintf("custom_field_%d[]", id), tc.value)
			}
			fieldPost(t, f.web, f.cookie, "/customers", values, 303)
			var recorded string
			historyCheck(t, f.pool.QueryRow(ctx, `SELECT custom_fields FROM customers WHERE display_name=$1`, "Scalar owner "+tc.kind).Scan(&recorded))
			query := "q=" + url.QueryEscape(tc.value) + "&type=customer"
			o := f.search(t, f.cookie, query)
			if o.Total < 1 {
				t.Fatalf("active scalar %s did not match", tc.kind)
			}
			def.Set("retired", "true")
			fieldPost(t, f.web, f.cookie, fmt.Sprintf("/settings/custom-fields/%d", id), def, 303)
			after := f.search(t, f.cookie, query)
			if after.Total != o.Total-1 {
				t.Fatalf("retirement did not remove exactly one hit: before=%+v after=%+v", o, after)
			}
			var retained string
			historyCheck(t, f.pool.QueryRow(ctx, `SELECT custom_fields FROM customers WHERE display_name=$1`, "Scalar owner "+tc.kind).Scan(&retained))
			discoveryEqual(t, retained, recorded)
			assertContains(t, retained, tc.value)
		})
	}
	for _, term := range []string{"Literal%106", "Literal_106", "O'Neil106", "Ünicode106"} {
		r := f.client.Item.Create().SetCompanyID(f.company).SetName(term).SaveX(ctx)
		discoveryEqual(t, f.search(t, f.cookie, "q="+url.QueryEscape(term)+"&type=item").IDs, []string{fmt.Sprintf("item:%d", r.ID)})
	}
	discoveryEqual(t, f.search(t, f.cookie, "q=LiteraX106&type=item").Total, 0)
}

func TestDiscoveryRetiredOptionOnActiveDefinitionHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	def := url.Values{"name": {"Recorded option106"}, "object_type": {"customer"}, "field_type": {"select"}, "options": {`["RetainedOption106","UnselectedOption106"]`}}
	fieldPost(t, f.web, f.cookie, "/settings/custom-fields", def, 303)
	var id int64
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT id FROM custom_field_definitions WHERE name='Recorded option106'`).Scan(&id))
	fieldPost(t, f.web, f.cookie, "/customers", url.Values{"display_name": {"Option owner"}, fmt.Sprintf("custom_field_%d", id): {"RetainedOption106"}}, 303)
	def.Set("retired_options", `["RetainedOption106"]`)
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/settings/custom-fields/%d", id), def, 303)
	discoveryEqual(t, f.search(t, f.cookie, "q=RetainedOption106&type=customer").Total, 1)
	discoveryEqual(t, f.search(t, f.cookie, "q=UnselectedOption106&type=customer").Total, 0)
	def.Set("retired", "true")
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/settings/custom-fields/%d", id), def, 303)
	discoveryEqual(t, f.search(t, f.cookie, "q=RetainedOption106&type=customer").Total, 0)
}

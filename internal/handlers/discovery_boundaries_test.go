package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestDiscoveryBuiltInScalarFacetAuthorityHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	item := f.client.Item.Create().SetCompanyID(f.company).SetName("Scalar item").SetType("product").SetUnitPrice(123.45).SetUnitCost(987.65).SetTaxRate("7.25").SaveX(ctx)
	_, role, cookie := f.reader(t, "ScalarFacet", authorization.Grant{Permission: "item.view", Scope: authorization.All})
	for _, term := range []string{"123.45", "987.65", "7.25"} {
		discoveryEqual(t, f.search(t, cookie, "q="+term).Total, 0)
	}
	discoveryEqual(t, f.search(t, cookie, "q=product").IDs, []string{fmt.Sprintf("item:%d", item.ID)})
	svc := authorization.New(f.pool)
	_, err := svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "ScalarFacet", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}}, 1)
	historyCheck(t, err)
	for _, term := range []string{"123.45", "7.25"} {
		discoveryEqual(t, f.search(t, cookie, "q="+term).IDs, []string{fmt.Sprintf("item:%d", item.ID)})
	}
	discoveryEqual(t, f.search(t, cookie, "q=987.65").Total, 0)
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "ScalarFacet", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "cost.view", Scope: authorization.All}}, 2)
	historyCheck(t, err)
	discoveryEqual(t, f.search(t, cookie, "q=987.65").IDs, []string{fmt.Sprintf("item:%d", item.ID)})
	for _, term := range []string{"123.45", "7.25"} {
		discoveryEqual(t, f.search(t, cookie, "q="+term).Total, 0)
	}
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Scalar date owner").SaveX(ctx)
	date := time.Date(2025, 2, 13, 0, 0, 0, 0, time.UTC)
	invoice := f.client.Invoice.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetTitle("Dated invoice").SetInvoiceNumber(1).SetInvoiceDate(date).SetDueDate(date.AddDate(0, 0, 10)).SaveX(ctx)
	discoveryEqual(t, f.search(t, f.cookie, "q=2025-02-13&type=invoice").IDs, []string{fmt.Sprintf("invoice:%d", invoice.ID)})
	discoveryEqual(t, f.search(t, cookie, "q=2025-02-13").Total, 0)
}

func TestDiscoveryAssignedScopeBeforeRankingHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Hidden Customer106").SaveX(ctx)
	for range 110 {
		f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Assigned106 hidden").SaveX(ctx)
	}
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Assigned106 visible").SaveX(ctx)
	uid, _, cookie := f.reader(t, "Assigned106", authorization.Grant{Permission: "job.view", Scope: authorization.Assigned}, authorization.Grant{Permission: "customer.view", Scope: authorization.Context})
	f.client.JobAssignment.Create().SetUserID(uid).SetJobID(j.ID).SaveX(ctx)
	o := f.search(t, cookie, "q=Assigned106")
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("job:%d", j.ID)})
	assertNotContains(t, strings.Join(o.Text, " "), "Hidden Customer106")
	discoveryEqual(t, f.search(t, cookie, "q=Hidden+Customer106").Total, 0)
	_, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, uid)
	historyCheck(t, err)
	discoveryEqual(t, f.search(t, cookie, "q=Assigned106").Total, 0)
}

func TestDiscoveryConversionHiddenNeverOrdinaryHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Conversion owner").SaveX(ctx)
	fieldPost(t, f.web, f.cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Conversion106"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	var estimateID int64
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT id FROM estimates WHERE title='Conversion106'`).Scan(&estimateID))
	path := fmt.Sprintf("/estimates/%d", estimateID)
	var sourceNumber string
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT number_label FROM estimates WHERE id=$1`, estimateID).Scan(&sourceNumber))
	before := f.search(t, f.cookie, "q=Conversion106&type=estimate")
	discoveryEqual(t, before.Total, 1)
	converted := fieldPost(t, f.web, f.cookie, path+"/convert", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	var targetNumber string
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT number_label FROM invoices WHERE title='Conversion106'`).Scan(&targetNumber))
	discoveryEqual(t, f.search(t, f.cookie, "q=Conversion106&type=estimate&state=all").Total, 0)
	discoveryEqual(t, f.search(t, f.cookie, "q="+url.QueryEscape(sourceNumber)+"&type=estimate&state=all").Total, 0)
	discoveryEqual(t, f.search(t, f.cookie, "q=Conversion106&type=invoice&state=all").Total, 1)
	fieldPost(t, f.web, f.cookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	discoveryEqual(t, f.search(t, f.cookie, "q=Conversion106&type=invoice&state=all").Total, 0)
	discoveryEqual(t, f.search(t, f.cookie, "q="+url.QueryEscape(targetNumber)+"&type=invoice&state=all").Total, 0)
	discoveryEqual(t, f.search(t, f.cookie, "q=Conversion106&type=estimate&state=all").IDs, before.IDs)
	var estimates, invoices int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM estimates WHERE title='Conversion106'`).Scan(&estimates))
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE title='Conversion106'`).Scan(&invoices))
	discoveryEqual(t, estimates, 1)
	discoveryEqual(t, invoices, 1)
}

func TestDiscoveryLeaseProjectionAndFilterIsolationHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("LeaseCustomer106").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises106").SetAddress1("106 Walnut Avenue").SaveX(ctx)
	typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Lease equipment").SaveX(ctx)
	a := f.client.Asset.Create().SetCompanyID(f.company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Lease equipment").SaveX(ctx)
	svc := authorization.New(f.pool)
	actor := authorization.Actor{UserID: f.admin}
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&today))
	start := today.AddDate(0, 0, 5).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "LeaseTitle106", StartsOn: start, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	zero, rent := int64(0), int64(10625)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	for _, term := range []string{"LeaseTitle106", "LeaseCustomer106", "Premises106", "Walnut"} {
		discoveryEqual(t, f.search(t, f.cookie, "q="+url.QueryEscape(term)+"&type=lease").IDs, []string{fmt.Sprintf("lease:%d", lease.ID)})
	}
	_, _, base := f.reader(t, "LeaseBase106", authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	discoveryEqual(t, f.search(t, base, "q=LeaseTitle106&type=lease").Total, 1)
	discoveryEqual(t, f.search(t, base, "q=106.25&type=lease").Total, 0)
	for _, queue := range []string{"monthly", "failed", "review"} {
		requestBody(t, f.web, base, "GET", "/search?q=&type=lease&queue="+queue, 403)
	}
	discoveryEqual(t, f.search(t, base, "q=&type=lease&lifecycle=scheduled").Total, 1)
	discoveryEqual(t, f.search(t, base, "q=&type=lease&lifecycle=active").Total, 0)
	_, _, billing := f.reader(t, "LeaseBilling106", authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	o := f.search(t, billing, "q=&type=lease")
	discoveryEqual(t, o.Total, 1)
	assertNotContains(t, strings.Join(o.Text, " "), "LeaseTitle106")
	assertNotContains(t, strings.Join(o.Text, " "), "Premises106")
	discoveryEqual(t, f.search(t, billing, "q=LeaseTitle106&type=lease").Total, 0)
}

func TestDiscoveryCommentsAndFileBytesExcludedHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Collaboration owner").SaveX(ctx)
	e := f.client.Estimate.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetTitle("Collaboration estimate").SaveX(ctx)
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/estimates/%d/comments", e.ID), url.Values{"content": {"CommentOnly106"}}, 200)
	conversionUpload(t, f.web, f.cookie, "estimate", e.ID, "ordinary.txt", "FileBytesOnly106")
	for _, term := range []string{"CommentOnly106", "FileBytesOnly106"} {
		discoveryEqual(t, f.search(t, f.cookie, "q="+term).Total, 0)
	}
	var comments, files int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE object_type='estimate' AND object_id=$1 AND content='CommentOnly106'`, e.ID).Scan(&comments))
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE object_type='estimate' AND object_id=$1`, e.ID).Scan(&files))
	discoveryEqual(t, comments, 1)
	discoveryEqual(t, files, 1)
}

package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"testing"
)

func TestCommercialTaxPreviewIsPurposeLimitedAndCurrent(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selected tax Customer").SetTaxExempt(true).SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Hidden tax Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned tax context").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Scoped tax preview").SetEmail("scoped-tax@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Scoped pricing preview", []authorization.Grant{{Permission: "customer.view", Scope: authorization.Context}, {Permission: "job.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/commercial/tax-preview?customer_id=%d&job_id=%d&rate=8.25", c.ID, j.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, `"rate":"0"`)
	for _, secret := range []string{"tax_exempt", "Selected tax Customer", "company_id"} {
		assertNotContains(t, body, secret)
	}
	requestBody(t, router, cookie, "GET", fmt.Sprintf("/commercial/tax-preview?customer_id=%d&job_id=%d&rate=8.25", other.ID, j.ID), 404)
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", path, 404)
}

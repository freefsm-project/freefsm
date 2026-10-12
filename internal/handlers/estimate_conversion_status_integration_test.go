package handlers

import (
	"context"
	"fmt"
	"testing"
)

func TestActiveEstimateWithoutStatusOffersConversionPreviewHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Statusless conversion customer").SaveX(ctx)
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Statusless conversion estimate").SaveX(ctx)

	detailPath := fmt.Sprintf("/estimates/%d", estimate.ID)
	body := requestBody(t, router, cookie, "GET", detailPath, 200)
	assertContains(t, body, fmt.Sprintf(`href="%s/convert"`, detailPath))
	assertContains(t, requestBody(t, router, cookie, "GET", detailPath+"/convert", 200), "Convert Estimate to Invoice")
}

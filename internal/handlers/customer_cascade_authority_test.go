package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCustomerCascadeWaitingChildViewAndArchiveRevocationHTTP(t *testing.T) {
	for _, missing := range []string{"contact.view", "contact.archive", "location.view", "location.archive", "asset.view", "asset.archive"} {
		t.Run(missing, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Revocation cascade").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			masterPost(t, web, adminCookie, base+"/contacts", url.Values{"first_name": {"Protected child"}, "is_primary": {"true"}}, 303)
			masterPost(t, web, adminCookie, base+"/locations", url.Values{"title": {"Protected place"}}, 303)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Protected equipment").SaveX(ctx)
			client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Protected equipment").SaveX(ctx)
			var grants []authorization.Grant
			for _, kind := range []string{"customer", "contact", "location", "asset"} {
				for _, action := range []string{"view", "archive"} {
					grants = append(grants, authorization.Grant{Permission: kind + "." + action, Scope: authorization.All})
				}
			}
			svc := authorization.New(pool)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting custodian", grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("waiting-cascade@test").SetName("Custodian").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			var review authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &review)
			tables := []string{"customers", "customer_contacts", "locations", "assets", "authorization_audit", "activity_logs"}
			before := historyDurableRows(t, pool, tables)
			tx, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(context.Background())
			_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, missing)
			historyCheck(t, err)
			var holder int
			historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- (&creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}).call(creditCommand{base + "/delete", url.Values{"archive_snapshot": {review.Snapshot}}})
			}()
			commercialWaiters(t, ctx, pool, holder, 1)
			t.Logf("observed grant revocation holder=%d waiting cascade permission=%s", holder, missing)
			historyCheck(t, tx.Commit(ctx))
			result := <-done
			if result.Code != 403 {
				t.Fatalf("revoked cascade returned %d %s", result.Code, result.Body.String())
			}
			historyEqual(t, "waiting grant loss rolls back all children and audit", historyDurableRows(t, pool, tables), before)
		})
	}
}

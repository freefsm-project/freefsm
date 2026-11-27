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

// Explicit Customer filters require a same-company, currently authorized target.
// Locking that target table pauses actual validation without a production hook.
// Both commit orders are exercised; successful pre-revocation state must remain
// blocked on restoration, and intrinsic Reset must still work after scope loss.
func TestDiscoverySensitivePreferenceCommitOrdersHTTP(t *testing.T) {
	for _, facet := range []string{"billing", "archived", "relation"} {
		for _, revokeFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/revoke_first=%v", facet, revokeFirst), func(t *testing.T) {
				f := newDiscoveryFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				svc := authorization.New(f.pool)
				admin := authorization.Actor{UserID: f.admin}
				customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Sensitive preference owner").SaveX(ctx)
				lease, err := svc.RegisterDraftLease(ctx, admin, customer.ID)
				historyCheck(t, err)
				historyCheck(t, svc.SaveDraftLeaseDetails(ctx, admin, lease.ID, authorization.DraftLeaseDetailsInput{Title: "SensitivePreference106"}))
				grants := []authorization.Grant{
					{Permission: "lease.view", Scope: authorization.All},
					{Permission: "customer.view", Scope: authorization.All},
				}
				query := url.Values{"q": {""}, "type": {"lease"}, "customer": {fmt.Sprint(customer.ID)}}
				remove := "customer.view"
				switch facet {
				case "billing":
					location := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Billing race premises").SaveX(ctx)
					assetType := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Billing race equipment").SaveX(ctx)
					asset := f.client.Asset.Create().SetCompanyID(f.company).SetOwnershipClass("company_owned").SetAssetTypeID(assetType.ID).SetName("Billing race equipment").SaveX(ctx)
					var today time.Time
					historyCheck(t, f.pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&today))
					start := today.AddDate(0, 0, 5).Format(time.DateOnly)
					historyCheck(t, svc.SaveDraftLeaseDetails(ctx, admin, lease.ID, authorization.DraftLeaseDetailsInput{Revision: 1, Title: "SensitivePreference106", StartsOn: start, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &location.ID, AssetIDs: []int64{asset.ID}}))
					zero, rent := int64(0), int64(10625)
					historyCheck(t, svc.SaveDraftLeaseBilling(ctx, admin, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
					historyCheck(t, svc.FinalizeLease(ctx, admin, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 2, BillingRevision: 1, ManagedBillingStart: start}))
					remove = "lease.billing.view"
					grants = append(grants, authorization.Grant{Permission: remove, Scope: authorization.All})
					query.Set("queue", "failed")
					_, err = f.pool.Exec(ctx, `INSERT INTO lease_billing_period_failures(lease_id,kind,due_on,failure) VALUES($1,'monthly',CURRENT_DATE,'Private fixture failure106')`, lease.ID)
					historyCheck(t, err)
				case "archived":
					remove = "lease.archived.view"
					grants = append(grants, authorization.Grant{Permission: remove, Scope: authorization.All})
					historyCheck(t, svc.LeaseLifecycle(ctx, admin, lease.ID, false))
					query.Set("state", "archived")
				}
				uid, role, cookie := f.reader(t, "Sensitive"+facet, grants...)
				discoveryEqual(t, f.search(t, cookie, query.Encode()).Total, 1)
				// Establish a real original revision and content. A rejected save
				// must preserve both, rather than merely avoiding an inserted row.
				discoverySaveOK(t, f, cookie, "q=prior106&type=lease", 0)
				actor := authorization.Actor{UserID: uid}
				before, err := svc.LoadListState(ctx, actor, "search")
				historyCheck(t, err)
				_, err = f.pool.Exec(ctx, `CREATE TABLE discovery_sensitive_order(id bigserial PRIMARY KEY,kind text NOT NULL);
				CREATE FUNCTION discovery_sensitive_capture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO discovery_sensitive_order(kind) VALUES(TG_ARGV[0]); RETURN NEW; END $$;
				CREATE TRIGGER discovery_sensitive_state BEFORE INSERT OR UPDATE ON list_states FOR EACH ROW EXECUTE FUNCTION discovery_sensitive_capture('state');
				CREATE TRIGGER discovery_sensitive_revoke BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION discovery_sensitive_capture('revoke');`)
				historyCheck(t, err)
				remaining := []authorization.Grant{}
				for _, grant := range grants {
					if grant.Permission != remove {
						remaining = append(remaining, grant)
					}
				}
				tx, err := f.pool.Begin(ctx)
				historyCheck(t, err)
				defer tx.Rollback(context.Background())
				var holder int
				historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder))
				if revokeFirst {
					_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`)
				} else {
					_, err = tx.Exec(ctx, `LOCK TABLE customers IN ACCESS EXCLUSIVE MODE`)
				}
				historyCheck(t, err)
				saved := make(chan *httptest.ResponseRecorder, 1)
				revoked := make(chan error, 1)
				save := func() { saved <- discoverySave(f, cookie, query.Encode(), before.Revision) }
				revoke := func() { _, err := svc.SaveRole(ctx, admin, role, "Sensitive"+facet, remaining, 1); revoked <- err }
				if revokeFirst {
					go revoke()
					discoveryWaitBlocked(t, ctx, f.pool, holder, 1)
					go save()
					discoveryWaitBlocked(t, ctx, f.pool, holder, 2)
				} else {
					go save()
					discoveryWaitBlocked(t, ctx, f.pool, holder, 1)
					var validator int
					historyCheck(t, f.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$1 AND $2=ANY(pg_blocking_pids(pid))`, f.pool.Config().ConnConfig.RuntimeParams["application_name"], holder).Scan(&validator))
					go revoke()
					discoveryWaitBlocked(t, ctx, f.pool, validator, 1)
				}
				historyCheck(t, tx.Commit(ctx))
				select {
				case err := <-revoked:
					historyCheck(t, err)
				case <-ctx.Done():
					t.Fatal("sensitive revocation blocked")
				}
				var response *httptest.ResponseRecorder
				select {
				case response = <-saved:
				case <-ctx.Done():
					t.Fatal("sensitive preference save blocked")
				}
				var order string
				historyCheck(t, f.pool.QueryRow(ctx, `SELECT coalesce(string_agg(kind,',' ORDER BY id),'') FROM discovery_sensitive_order`).Scan(&order))
				after, err := svc.LoadListState(ctx, actor, "search")
				historyCheck(t, err)
				switch response.Code {
				case 204:
					if revokeFirst || order != "state,revoke" {
						t.Fatalf("unauthorized preference success: revoke_first=%v order=%s", revokeFirst, order)
					}
					discoveryEqual(t, after.Revision, before.Revision+1)
					for key, values := range query {
						discoveryEqual(t, after.Values[key], values)
					}
					requestBody(t, f.web, cookie, "GET", "/search", 403)
				case 403:
					discoveryEqual(t, order, "revoke")
					discoveryEqual(t, after, before)
				default:
					t.Fatalf("save status=%d body=%s order=%s", response.Code, response.Body.String(), order)
				}
				requestBody(t, f.web, cookie, "GET", "/search?"+query.Encode(), 403)
				// Reset is an intrinsic own-user operation, even when the saved
				// query can no longer be authorized.
				discoveryReset(t, f, cookie, after.Revision, 303)
				cleared, err := svc.LoadListState(ctx, actor, "search")
				historyCheck(t, err)
				discoveryEqual(t, len(cleared.Values), 0)
				discoveryEqual(t, cleared.Revision, after.Revision+1)
			})
		}
	}
}

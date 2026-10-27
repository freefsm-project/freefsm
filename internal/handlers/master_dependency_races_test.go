package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestMasterWorkLinkArchiveCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"contact", "location", "asset"} {
		for _, first := range []string{"link", "archive"} {
			t.Run(kind+"/"+first, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Link race owner").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unfinished link race").SaveX(ctx)
				var id int64
				table, column := "assets", "asset_id"
				switch kind {
				case "contact":
					id = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Link race contact").SaveX(ctx).ID
					table, column = "customer_contacts", "customer_contact_id"
				case "location":
					id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Link race site").SaveX(ctx).ID
					table, column = "locations", "location_id"
				case "asset":
					typ := client.AssetType.Create().SetCompanyID(company).SetName("Link equipment").SaveX(ctx)
					id = client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Link race asset").SaveX(ctx).ID
				}
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder int
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				link := `UPDATE jobs SET ` + column + `=$1 WHERE id=$2`
				if first == "link" {
					_, err = barrier.Exec(ctx, link, id, j.ID)
					historyCheck(t, err)
					f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() { done <- f.call(creditCommand{path: fmt.Sprintf("/%ss/%d/delete", kind, id)}) }()
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, barrier.Commit(ctx))
					if r := <-done; r.Code != 409 {
						t.Fatalf("archive=%d: %s", r.Code, r.Body.String())
					}
				} else {
					_, err = barrier.Exec(ctx, `UPDATE `+table+` SET deleted_at=clock_timestamp() WHERE id=$1`, id)
					historyCheck(t, err)
					done := make(chan error, 1)
					go func() { _, err := pool.Exec(ctx, link, id, j.ID); done <- err }()
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, barrier.Commit(ctx))
					if err := <-done; err == nil {
						t.Fatal("waiting work writer linked archived master")
					}
				}
				t.Logf("observed %s/%s holder %d and blocked waiter", kind, first, holder)
			})
		}
	}
}

func TestMasterPlacementCoverageCommitOrdersHTTP(t *testing.T) {
	for _, first := range []string{"coverage", "placement"} {
		t.Run(first, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Covered Customer").SaveX(ctx)
			other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Placement Customer").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Race equipment").SaveX(ctx)
			query := `SELECT set_lease_master_dependencies(63,$1,$2,NULL,'scheduled',false,$3::jsonb)`
			coverage := fmt.Sprintf(`[{"asset_id":%d,"starts_on":"2099-01-01"}]`, a.ID)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			place := creditCommand{path: fmt.Sprintf("/assets/%d/associations/place", a.ID), values: url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "revision": {"0"}, "reason": {"Observed race placement"}}}
			placed := make(chan *httptest.ResponseRecorder, 1)
			if first == "coverage" {
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				_, err = barrier.Exec(ctx, query, company, c.ID, coverage)
				historyCheck(t, err)
				go func() { placed <- f.call(place) }()
				commercialWaiters(t, ctx, pool, holder, 1)
				historyCheck(t, barrier.Commit(ctx))
				if r := <-placed; r.Code != 409 {
					t.Fatalf("placement=%d: %s", r.Code, r.Body.String())
				}
			} else {
				// Hold only the Asset row. The production placement owns the global
				// fence while waiting on this row, so coverage must commit second.
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM assets WHERE id=$1 FOR UPDATE`, a.ID).Scan(&holder))
				go func() { placed <- f.call(place) }()
				var placementPID int
				for placementPID == 0 {
					historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(min(pid),0) FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&placementPID))
					if placementPID == 0 {
						select {
						case <-ctx.Done():
							t.Fatal("placement did not reach row barrier")
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				covered := make(chan error, 1)
				go func() { _, err := pool.Exec(ctx, query, company, c.ID, coverage); covered <- err }()
				commercialWaiters(t, ctx, pool, placementPID, 1)
				historyCheck(t, barrier.Commit(ctx))
				if r := <-placed; r.Code != 303 {
					t.Fatalf("placement=%d: %s", r.Code, r.Body.String())
				}
				if err := <-covered; err == nil {
					t.Fatal("coverage accepted another Customer placement")
				}
			}
			t.Logf("observed %s-first placement/coverage ordering, holder=%d", first, holder)
		})
	}
}

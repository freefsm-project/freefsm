package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
)

func TestLeaseRetainedTimelineCorrectionsHTTP(t *testing.T) {
	for _, archived := range []bool{false, true} {
		for _, kind := range []string{"placement", "origin", "return"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, kind), func(t *testing.T) {
				f := newLeaseReviewFixture(t)
				returned, err := time.Parse(time.DateOnly, f.returned)
				historyCheck(t, err)
				end := returned.AddDate(0, 0, -1).Format(time.DateOnly)
				in := f.dependency()
				in.Lifecycle, in.Archived, in.Coverage[0].EndsOn = "terminated", archived, &end
				if kind == "origin" {
					typ := f.client.Asset.GetX(f.ctx, f.asset).AssetTypeID
					a := f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(f.a).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Unknown-start original observation").SaveX(f.ctx)
					f.asset, f.event = a.ID, 0
					in.Coverage[0].AssetID = a.ID
					fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/return", a.ID), url.Values{"effective_date": {f.returned}, "reason": {"Recorded return"}, "revision": {"0"}}, 303)
				}
				if kind == "return" {
					in.CustomerID = f.b
					in.Coverage[0].StartsOn = f.returned
					end = returned.AddDate(0, 0, 3).Format(time.DateOnly)
				}
				historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
				command := f.correction()
				if kind == "origin" {
					command.values.Set("revision", "1")
				}
				if kind == "return" {
					var event int64
					historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='return'`, f.asset).Scan(&event))
					command.values = url.Values{"event_id": {fmt.Sprint(event)}, "effective_date": {returned.AddDate(0, 0, 1).Format(time.DateOnly)}, "reason": {"Correct actual return"}, "revision": {"2"}}
				}
				before := f.snapshot(t)
				fieldPost(t, f.web, f.cookie, command.path, command.values, 409)
				historyEqual(t, "retained coverage rejects correction without changing original facts or audit", f.snapshot(t), before)

				// The same deferred validator protects SQL event and origin writers.
				tx, err := f.pool.Begin(f.ctx)
				historyCheck(t, err)
				defer tx.Rollback(context.Background())
				if kind == "origin" {
					_, err = tx.Exec(f.ctx, `INSERT INTO asset_association_origin_revisions(asset_id,effective_date,customer_id,reason,actor_id,before_fact) SELECT $1,$2,$3,'Explicit origin correction',$4,to_jsonb(o) FROM asset_association_origin_facts o WHERE asset_id=$1`, f.asset, f.start, f.b, f.admin)
				} else {
					var customer any = f.b
					if kind == "return" {
						customer = nil
					}
					_, err = tx.Exec(f.ctx, `INSERT INTO asset_association_events(asset_id,event_id,action,effective_date,customer_id,reason,actor_id,before_fact) SELECT $1,event_id,'correct',$3,$4,'Explicit SQL correction',$5,to_jsonb(f) FROM asset_association_facts f WHERE asset_id=$1 AND event_id=$2`, f.asset, command.values.Get("event_id"), command.values.Get("effective_date"), customer, f.admin)
				}
				historyCheck(t, err)
				err = tx.Commit(f.ctx)
				if err == nil {
					t.Fatal("SQL correction contradicted retained coverage")
				}
				if kind == "origin" && !strings.Contains(err.Error(), "explicit mapping review") {
					t.Fatalf("unknown observation start should require review, not fabricated overlap: %v", err)
				}
				historyEqual(t, "SQL rejection conserves history and audit", f.snapshot(t), before)
				// A new B placement strictly after the inclusive end is legitimate.
				revision := "2"
				if kind == "origin" {
					revision = "1"
				}
				fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/place", f.asset), url.Values{"customer_id": {fmt.Sprint(f.b)}, "effective_date": {returned.AddDate(0, 0, 4).Format(time.DateOnly)}, "reason": {"New placement after ended coverage"}, "revision": {revision}}, 303)
				revision = "3"
				if kind == "origin" {
					revision = "2"
				}
				fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/return", f.asset), url.Values{"effective_date": {returned.AddDate(0, 0, 5).Format(time.DateOnly)}, "reason": {"Return before archive"}, "revision": {revision}}, 303)
				fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/delete", f.asset), nil, 303)
			})
		}
	}
}

func TestLeaseRetainedTimelineDisjointControlsHTTP(t *testing.T) {
	for _, archived := range []bool{false, true} {
		for _, boundary := range []string{"end-before-placement", "start-at-return", "empty-placement", "same-customer"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, boundary), func(t *testing.T) {
				f := newLeaseReviewFixture(t)
				start, err := time.Parse(time.DateOnly, f.start)
				historyCheck(t, err)
				returned, err := time.Parse(time.DateOnly, f.returned)
				historyCheck(t, err)
				in := f.dependency()
				end := returned.AddDate(0, 0, -1).Format(time.DateOnly)
				in.Lifecycle, in.Archived, in.Coverage[0].EndsOn = "terminated", archived, &end
				command := f.correction()
				switch boundary {
				case "end-before-placement":
					in.Coverage[0].StartsOn = start.AddDate(0, 0, -2).Format(time.DateOnly)
					end = start.AddDate(0, 0, -1).Format(time.DateOnly)
				case "start-at-return":
					in.Coverage[0].StartsOn = f.returned
					end = returned.AddDate(0, 0, 1).Format(time.DateOnly)
				case "empty-placement":
					var event int64
					historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='return'`, f.asset).Scan(&event))
					fieldPost(t, f.web, f.cookie, command.path, url.Values{"event_id": {fmt.Sprint(event)}, "effective_date": {f.start}, "reason": {"Actual same-day return"}, "revision": {"2"}}, 303)
					command.values.Set("revision", "3")
				case "same-customer":
					command.values.Set("customer_id", fmt.Sprint(f.a))
				}
				historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
				before := f.snapshot(t)
				fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
				after := f.snapshot(t)
				for _, table := range []string{"lease_master_dependencies", "lease_asset_dependencies", "asset_association_origins"} {
					historyEqual(t, "legitimate correction retains "+table, after[table], before[table])
				}
				var original bool
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT customer_id=$2 AND effective_date=$3::date FROM asset_association_events WHERE id=$1`, f.event, f.a, f.start).Scan(&original))
				if !original {
					t.Fatal("correction rewrote original placement evidence")
				}
			})
		}
	}
}

func TestLeaseRetainedTimelineUnknownBoundsRequireReview(t *testing.T) {
	f := newLeaseReviewFixture(t)
	in := f.dependency()
	in.Lifecycle, in.Coverage[0].EndsOn = "terminated", nil
	before := f.snapshot(t)
	err := f.register(authorization.Actor{UserID: f.admin}, in)
	if err == nil || !strings.Contains(err.Error(), "explicit mapping review") {
		t.Fatalf("unknown termination end was invented or ignored: %v", err)
	}
	historyEqual(t, "unknown bounds remain recoverable without fabricated coverage", f.snapshot(t), before)
}

func TestLeaseRetainedTimelineUnknownOriginDisjointHTTP(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprint(archived), func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			typ := f.client.Asset.GetX(f.ctx, f.asset).AssetTypeID
			a := f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(f.a).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Observed start stays unknown").SaveX(f.ctx)
			f.asset, f.event = a.ID, 0
			fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/return", a.ID), url.Values{"effective_date": {f.returned}, "reason": {"Known return boundary"}, "revision": {"0"}}, 303)
			in := f.dependency()
			in.Lifecycle, in.Archived = "terminated", archived
			in.Coverage[0].StartsOn, in.Coverage[0].EndsOn = f.returned, &f.returned
			historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
			command := f.correction()
			command.values.Set("revision", "1")
			fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
			var valid bool
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT p.starts_on IS NULL AND p.ends_before=$2::date AND p.customer_id=$3 AND o.customer_id=$4 FROM asset_association_periods p JOIN asset_association_origins o ON o.asset_id=p.asset_id WHERE p.asset_id=$1 AND p.event_id=0`, a.ID, f.returned, f.b, f.a).Scan(&valid))
			if !valid {
				t.Fatal("disjoint origin correction fabricated dates or rewrote original observation")
			}
		})
	}
}

func TestLeaseRetainedTimelineReviewedUpgrade(t *testing.T) {
	for _, problem := range []string{"conflicting-placement", "unknown-termination-end"} {
		t.Run(problem, func(t *testing.T) {
			prior := fstest.MapFS{}
			entries, err := fs.ReadDir(database.MigrationFS(), ".")
			historyCheck(t, err)
			for _, entry := range entries {
				if entry.Name() >= "112_" {
					continue
				}
				raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
				historyCheck(t, err)
				prior[entry.Name()] = &fstest.MapFile{Data: raw}
			}
			pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
			ctx := context.Background()
			f := &leaseReviewFixture{ctx: ctx, pool: pool, client: client, web: web, cookie: sessionCookie(t, ctx, sessions, admin), company: company, admin: admin, start: "2000-01-01", returned: "2000-02-01", end: "2000-01-31"}
			f.a = client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained agreement Customer").SaveX(ctx).ID
			f.b = client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical correction Customer").SaveX(ctx).ID
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			f.asset = client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Legacy retained history").SaveX(ctx).ID
			path := fmt.Sprintf("/assets/%d/associations/", f.asset)
			fieldPost(t, web, f.cookie, path+"place", url.Values{"customer_id": {fmt.Sprint(f.a)}, "effective_date": {f.start}, "reason": {"Original fact"}, "revision": {"0"}}, 303)
			fieldPost(t, web, f.cookie, path+"return", url.Values{"effective_date": {f.returned}, "reason": {"Actual return"}, "revision": {"1"}}, 303)
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='place'`, f.asset).Scan(&f.event))
			in := f.dependency()
			in.Lifecycle, in.Archived = "terminated", true
			if problem == "unknown-termination-end" {
				in.Coverage[0].EndsOn = nil
			}
			historyCheck(t, f.register(authorization.Actor{UserID: admin}, in))
			command := f.correction()
			if problem == "conflicting-placement" {
				// Schema 111 accepted this historical contradiction; do not disable guards.
				fieldPost(t, web, f.cookie, command.path, command.values, 303)
			}
			before := f.snapshot(t)
			err = (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS())
			if err == nil || !strings.Contains(err.Error(), "explicit mapping review") {
				t.Fatalf("incompatible retained history must stop for review: %v", err)
			}
			historyEqual(t, "failed upgrade never changes known facts or invents dates", f.snapshot(t), before)
			// Explicit owning corrections leave originals and before/after audit intact.
			if problem == "conflicting-placement" {
				command.values.Set("customer_id", fmt.Sprint(f.a))
				command.values.Set("revision", "3")
				fieldPost(t, web, f.cookie, command.path, command.values, 303)
			} else {
				in.Coverage[0].EndsOn = &f.end
				historyCheck(t, f.register(authorization.Actor{UserID: admin}, in))
			}
			before = f.snapshot(t)
			historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
			historyEqual(t, "reviewed upgrade preserves original history and correction evidence", f.snapshot(t), before)
			var original bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id=$2 AND effective_date=$3::date FROM asset_association_events WHERE id=$1`, f.event, f.a, f.start).Scan(&original))
			if !original {
				t.Fatal("review replaced original placement fact")
			}
		})
	}
}

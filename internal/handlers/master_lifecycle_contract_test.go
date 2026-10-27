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

func TestMasterRestoreWaitingAuthorityAndParentsHTTP(t *testing.T) {
	masterLifecycleWaitingAuthority(t, true)
}

func TestMasterArchiveWaitingAuthorityHTTP(t *testing.T) {
	masterLifecycleWaitingAuthority(t, false)
}

func masterLifecycleWaitingAuthority(t *testing.T, restore bool) {
	action, suffix := "archive", "/delete"
	if restore {
		action, suffix = "restore", "/restore"
	}
	for _, kind := range []string{"contact", "location", "asset"} {
		changes := []string{"grant", "session"}
		if restore {
			changes = append(changes, "customer")
		}
		if restore && kind == "asset" {
			changes = append(changes, "location")
		}
		for _, change := range changes {
			t.Run(kind+"/"+change, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Master owner").SaveX(ctx)
				location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Current Asset location").SaveX(ctx)
				var id int64
				table := "assets"
				switch kind {
				case "contact":
					id = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Primary person").SaveX(ctx).ID
					_, err := pool.Exec(ctx, `UPDATE customer_contacts SET is_primary=true WHERE id=$1`, id)
					historyCheck(t, err)
					table = "customer_contacts"
				case "location":
					id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Primary place").SetIsPrimary(true).SaveX(ctx).ID
					table = "locations"
				case "asset":
					typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
					id = client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(location.ID).SetOwnershipClass("customer_owned").SetName("Owned asset").SetAssetTypeID(typ.ID).SaveX(ctx).ID
				}
				path := fmt.Sprintf("/%ss/%d", kind, id)
				adminCookie := sessionCookie(t, ctx, sessions, admin)
				fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
				if !restore {
					fieldPost(t, web, adminCookie, path+"/restore", nil, 303)
				}
				svc := authorization.New(pool)
				role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent custodian", []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".archived.view", Scope: authorization.All}, {Permission: kind + "." + action, Scope: authorization.All}})
				historyCheck(t, err)
				u := client.User.Create().SetCompanyID(company).SetName("Restorer").SetEmail("master63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
				historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
				cookie := sessionCookie(t, ctx, sessions, u.ID)
				before := historyDurableRows(t, pool, []string{table, "authorization_audit", "activity_logs"})
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder int
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.call(creditCommand{path: path + suffix}) }()
				commercialWaiters(t, ctx, pool, holder, 1)
				want := 403
				switch change {
				case "grant":
					_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, kind+"."+action)
				case "session":
					_, err = barrier.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, u.ID)
				case "customer":
					_, err = barrier.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID)
					want = 409
				case "location":
					_, err = barrier.Exec(ctx, `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, location.ID)
					want = 409
				}
				historyCheck(t, err)
				historyCheck(t, barrier.Commit(ctx))
				response := <-done
				if response.Code != want {
					t.Fatalf("%s=%d want%d: %s", action, response.Code, want, response.Body.String())
				}
				historyEqual(t, "denied lifecycle is atomic", historyDurableRows(t, pool, []string{table, "authorization_audit", "activity_logs"}), before)
			})
		}
	}
}

func TestMasterPrimaryArchiveAuditAndIndependentRestoreHTTP(t *testing.T) {
	for _, kind := range []string{"contact", "location"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Primary owner").SaveX(ctx)
			values := url.Values{"first_name": {"Original primary"}, "is_primary": {"true"}}
			table := "customer_contacts"
			if kind == "location" {
				values = url.Values{"title": {"Original primary"}, "is_primary": {"true"}}
				table = "locations"
			}
			base := fmt.Sprintf("/customers/%d/%ss", c.ID, kind)
			path := masterPost(t, web, cookie, base, values, 303)
			var child authorization.CustomerChild
			masterJSON(t, web, cookie, path, &child)
			before := historyDurableRows(t, pool, []string{table, "authorization_audit", "activity_logs"})
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_master_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s.archive' THEN RAISE EXCEPTION 'injected master audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_master_lifecycle_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_master_lifecycle_audit()`, kind))
			historyCheck(t, err)
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			if r := f.call(creditCommand{path: path + "/delete"}); r.Code < 400 {
				t.Fatal("audit failure accepted archive")
			}
			historyEqual(t, "failed archive retains primary and history", historyDurableRows(t, pool, []string{table, "authorization_audit", "activity_logs"}), before)
			_, err = pool.Exec(ctx, `DROP TRIGGER reject_master_lifecycle_audit ON authorization_audit; DROP FUNCTION reject_master_lifecycle_audit()`)
			historyCheck(t, err)
			masterPost(t, web, cookie, path+"/delete", nil, 303)
			var primary bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT is_primary FROM `+table+` WHERE id=$1`, child.ID).Scan(&primary))
			if primary {
				t.Fatal("archive retained primary default")
			}
			var audits int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type=$1 AND target_id=$2 AND action=$3 AND detail->'before'->>'is_primary'='true' AND detail->'after'->>'is_primary'='false'`, kind, child.ID, kind+".archive").Scan(&audits))
			if audits != 1 {
				t.Fatalf("actual primary transition audit count=%d", audits)
			}
			// Explicitly choose a different primary; Restore must not replace it.
			otherPath := masterPost(t, web, cookie, base, values, 303)
			var other authorization.CustomerChild
			masterJSON(t, web, cookie, otherPath, &other)
			svc := authorization.New(pool)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Restore only", []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".archived.view", Scope: authorization.All}, {Permission: kind + ".restore", Scope: authorization.All}})
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetName("Independent restorer").SetEmail("primary63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			masterPost(t, web, sessionCookie(t, ctx, sessions, u.ID), fmt.Sprintf("/%ss/%d/restore", kind, child.ID), nil, 303)
			historyCheck(t, pool.QueryRow(ctx, `SELECT is_primary FROM `+table+` WHERE id=$1`, child.ID).Scan(&primary))
			if primary {
				t.Fatal("Restore reinstated former primary")
			}
			historyCheck(t, pool.QueryRow(ctx, `SELECT is_primary FROM `+table+` WHERE id=$1`, other.ID).Scan(&primary))
			if !primary {
				t.Fatal("Restore replaced current primary")
			}
		})
	}
}

func TestMasterChildArchiveLegacyWriterGuards(t *testing.T) {
	for _, kind := range []string{"contact", "location"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Work owner").SaveX(ctx)
			table, column := "customer_contacts", "customer_contact_id"
			var id int64
			if kind == "contact" {
				id = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Work contact").SaveX(ctx).ID
			} else {
				id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Work site").SaveX(ctx).ID
				table, column = "locations", "location_id"
			}
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unfinished").SaveX(ctx)
			_, err := pool.Exec(ctx, `UPDATE jobs SET `+column+`=$1 WHERE id=$2`, id, j.ID)
			historyCheck(t, err)
			if kind == "contact" {
				// A legacy writer can remove the join projection while retaining
				// the scalar compatibility link. It must not bypass archive.
				_, err = pool.Exec(ctx, `DELETE FROM job_contacts WHERE job_id=$1`, j.ID)
				historyCheck(t, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE `+table+` SET deleted_at=clock_timestamp() WHERE id=$1`, id); err == nil {
				t.Fatal("legacy writer bypassed unfinished Job blocker")
			}
			var completed int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&completed))
			masterPost(t, web, cookie, fmt.Sprintf("/jobs/%d/status", j.ID), url.Values{"status_id": {fmt.Sprint(completed)}}, 303)
			path := fmt.Sprintf("/%ss/%d", kind, id)
			masterPost(t, web, cookie, path+"/delete", nil, 303)
			if kind == "contact" {
				if _, err := pool.Exec(ctx, `INSERT INTO job_contacts(job_id,contact_id) VALUES($1,$2)`, j.ID, id); err == nil {
					t.Fatal("legacy link writer selected archived Contact")
				}
			}
			masterPost(t, web, cookie, path+"/restore", nil, 303)
			var retained int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT `+column+` FROM jobs WHERE id=$1`, j.ID).Scan(&retained))
			if retained != id {
				t.Fatal("archive/Restore rewrote finished historical link")
			}
		})
	}
}

func TestMasterLifecycleIndependentGrantsAndAuditFailureHTTP(t *testing.T) {
	for _, kind := range []string{"contact", "location", "asset"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent lifecycle owner").SaveX(ctx)
			var id int64
			table := "assets"
			switch kind {
			case "contact":
				id = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Independent person").SaveX(ctx).ID
				table = "customer_contacts"
			case "location":
				id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Independent place").SaveX(ctx).ID
				table = "locations"
			case "asset":
				typ := client.AssetType.Create().SetCompanyID(company).SetName("Independent equipment").SaveX(ctx)
				id = client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Independent asset").SaveX(ctx).ID
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
			fieldPost(t, web, adminCookie, path+"/restore", nil, 303)
			svc := authorization.New(pool)
			grants := []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".archived.view", Scope: authorization.All}, {Permission: kind + ".archive", Scope: authorization.All}}
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent archive", grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetName("Independent custodian").SetEmail("audit63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			for _, action := range []string{"archive", "restore"} {
				suffix := "/delete"
				if action == "restore" {
					fieldPost(t, web, cookie, path+"/restore", nil, 403)
					grants[2].Permission = kind + ".restore"
					_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Independent restore", grants, 1)
					historyCheck(t, err)
					suffix = "/restore"
				}
				before := historyDurableRows(t, pool, []string{table, "authorization_audit", "activity_logs"})
				_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_master_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s.%s' THEN RAISE EXCEPTION 'injected master lifecycle audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_master_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_master_audit()`, kind, action))
				historyCheck(t, err)
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				if r := f.call(creditCommand{path: path + suffix}); r.Code < 400 {
					t.Fatalf("%s audit failure committed", action)
				}
				historyEqual(t, "failed lifecycle is atomic", historyDurableRows(t, pool, []string{table, "authorization_audit", "activity_logs"}), before)
				_, err = pool.Exec(ctx, `DROP TRIGGER fail_master_audit ON authorization_audit; DROP FUNCTION fail_master_audit()`)
				historyCheck(t, err)
				fieldPost(t, web, cookie, path+suffix, nil, 303)
			}
			fieldPost(t, web, cookie, path+"/delete", nil, 403)
		})
	}
}

func TestMasterLocationProjectArchiveUsesCategoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Project owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Project site").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetName("Unfinished despite progress").SetCompletionPercentage(100).SaveX(ctx)
	path := fmt.Sprintf("/locations/%d", l.ID)
	fieldPost(t, web, cookie, path+"/delete", nil, 409)
	if _, err := pool.Exec(ctx, `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, l.ID); err == nil {
		t.Fatal("progress100 bypassed unfinished Project guard")
	}
	var completed int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed' AND is_category_default`, company).Scan(&completed))
	masterPost(t, web, cookie, fmt.Sprintf("/projects/%d/status", p.ID), url.Values{"status_id": {fmt.Sprint(completed)}}, 303)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	var retained int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT location_id FROM projects WHERE id=$1`, p.ID).Scan(&retained))
	if retained != l.ID {
		t.Fatal("finished Project historical Location rewritten")
	}
}

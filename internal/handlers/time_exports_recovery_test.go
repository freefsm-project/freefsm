package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimeExportRecoveryOriginsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	user := client.User.Create().SetCompanyID(company).SetEmail("export-recovery@test").SetName("Export recovery").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	role, err := auth.SaveRole(ctx, actor, 0, "Time recovery", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.export", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	path := prepareTimeExport(t, pool, router, cookie, url.Values{"scope": {"O"}, "search": {"PRIVATE old filter"}})
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(path, "/exports/")); err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct {
		path string
		code int
	}{{path, 200}, {path + "/download", 410}} {
		body := requestBody(t, router, cookie, "GET", page.path, page.code)
		assertContains(t, body, `href="/time-entries/export"`)
		assertContains(t, body, "Generate a new Timesheet export")
		assertNotContains(t, body, `href="/time-entries/export?type=gps"`)
		assertNotContains(t, body, "PRIVATE old filter")
	}
	review := requestBody(t, router, cookie, "GET", "/time-entries/export", 200)
	assertContains(t, review, "Confirm Timesheet export")
	assertNotContains(t, review, "PRIVATE old filter")
}

func assertTimeRecoveryOrigins(t *testing.T, body string, timeAllowed, gpsAllowed bool) {
	t.Helper()
	for _, link := range []struct {
		href    string
		allowed bool
	}{{`href="/time-entries/export"`, timeAllowed}, {`href="/time-entries/export?type=gps"`, gpsAllowed}} {
		if strings.Contains(body, link.href) != link.allowed {
			t.Fatalf("recovery link %s present=%v, want %v", link.href, strings.Contains(body, link.href), link.allowed)
		}
	}
	assertNotContains(t, body, "PRIVATE old filter")
}

func failedTimeRecoveryExport(t *testing.T, pool *pgxpool.Pool, router http.Handler, cookie *http.Cookie, kind string) string {
	t.Helper()
	ctx := context.Background()
	path := masterPost(t, router, cookie, "/time-entries/export", url.Values{"operation_id": {newOperationKey()}, "type": {kind}, "search": {"PRIVATE old filter"}}, 303)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_recovery_export() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.detail->>'event'='generated' THEN RAISE EXCEPTION 'injected generation failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_recovery_export BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_recovery_export()`); err != nil {
		t.Fatal(err)
	}
	_, generationErr := exports.Product(pool).ProcessOne(ctx)
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_recovery_export ON authorization_audit; DROP FUNCTION fail_recovery_export()`); err != nil {
		t.Fatal(err)
	}
	if generationErr == nil {
		t.Fatal("generation fault did not fail")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
	return path
}

func TestTimeExportRecoveryAuthorityMatrixHTTP(t *testing.T) {
	for _, scope := range []authorization.Scope{authorization.Own, authorization.Assigned, authorization.All} {
		for _, kind := range []string{"time", "gps"} {
			t.Run(string(scope)+"/"+kind, func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				auth := authorization.New(pool)
				actor := authorization.Actor{UserID: admin}
				user := client.User.Create().SetCompanyID(company).SetEmail("recovery-matrix@test").SetName("Recovery matrix").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
				grants := []authorization.Grant{{Permission: "time.view", Scope: scope}}
				permission := "time.export"
				if kind == "gps" {
					permission = "time.gps.export"
					grants = append(grants, authorization.Grant{Permission: "time.gps.view", Scope: scope})
				}
				viewGrants := append([]authorization.Grant(nil), grants...)
				grants = append(grants, authorization.Grant{Permission: permission, Scope: scope})
				role, err := auth.SaveRole(ctx, actor, 0, "Recovery matrix", grants)
				if err != nil {
					t.Fatal(err)
				}
				if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
					t.Fatal(err)
				}
				cookie := sessionCookie(t, ctx, sessions, user.ID)
				ready := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {kind}, "search": {"PRIVATE old filter"}})
				expired := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {kind}, "search": {"PRIVATE old filter"}})
				if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(expired, "/exports/")); err != nil {
					t.Fatal(err)
				}
				failed := failedTimeRecoveryExport(t, pool, router, cookie, kind)
				for _, page := range []struct {
					path string
					code int
				}{{expired, 200}, {expired + "/download", 410}, {failed, 200}} {
					body := requestBody(t, router, cookie, "GET", page.path, page.code)
					assertTimeRecoveryOrigins(t, body, kind == "time", kind == "gps")
					assertNotContains(t, body, "Generate a new activity export")
					assertNotContains(t, body, `href="/customers/export"`)
				}
				target := "/time-entries/export"
				if kind == "gps" {
					target += "?type=gps"
				}
				review := requestBody(t, router, cookie, "GET", target, 200)
				assertContains(t, review, fmt.Sprintf(`value="%s" selected`, scope))
				assertNotContains(t, review, "PRIVATE old filter")
				// Current View without Export must remove every regeneration action,
				// even though the requester retains the old nonsensitive disposition.
				var revision int64
				if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
					t.Fatal(err)
				}
				if _, err := auth.SaveRole(ctx, actor, role, "Recovery matrix", viewGrants, revision); err != nil {
					t.Fatal(err)
				}
				assertTimeRecoveryOrigins(t, requestBody(t, router, cookie, "GET", ready+"/download", 410), false, false)
				assertTimeRecoveryOrigins(t, requestBody(t, router, cookie, "GET", ready, 200), false, false)
				assertTimeRecoveryOrigins(t, requestBody(t, router, cookie, "GET", failed, 200), false, false)
				expectStatus(t, router, cookie, "GET", target, 403)
			})
		}
	}
}

func TestTimeExportRecoveryAfterGPSLossHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	user := client.User.Create().SetCompanyID(company).SetEmail("recovery-gps-loss@test").SetName("Recovery GPS loss").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	timeGrants := []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.export", Scope: authorization.All}}
	grants := append(append([]authorization.Grant(nil), timeGrants...), authorization.Grant{Permission: "time.gps.view", Scope: authorization.All}, authorization.Grant{Permission: "time.gps.export", Scope: authorization.All})
	role, err := auth.SaveRole(ctx, actor, 0, "Recovery GPS loss", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	gps := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {"gps"}, "search": {"PRIVATE old filter"}})
	timePath := prepareTimeExport(t, pool, router, cookie, url.Values{})
	assertTimeRecoveryOrigins(t, requestBody(t, router, cookie, "GET", gps, 200), true, true)
	// The protected broad user also receives relevant origins on a failed result.
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	failed := failedTimeRecoveryExport(t, pool, router, adminCookie, "gps")
	assertTimeRecoveryOrigins(t, requestBody(t, router, adminCookie, "GET", failed, 200), true, true)
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, actor, role, "Recovery GPS loss", timeGrants, revision); err != nil {
		t.Fatal(err)
	}
	assertTimeRecoveryOrigins(t, requestBody(t, router, cookie, "GET", gps+"/download", 410), true, false)
	assertTimeRecoveryOrigins(t, requestBody(t, router, cookie, "GET", gps, 200), true, false)
	requestBody(t, router, cookie, "GET", timePath+"/download", 200)
	review := requestBody(t, router, cookie, "GET", "/time-entries/export", 200)
	assertContains(t, review, "Confirm Timesheet export")
	assertNotContains(t, review, "PRIVATE old filter")
	newPath := prepareTimeExport(t, pool, router, cookie, url.Values{})
	requestBody(t, router, cookie, "GET", newPath+"/download", 200)
}

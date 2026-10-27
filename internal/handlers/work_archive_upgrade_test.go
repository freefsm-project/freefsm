package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
)

func TestWorkArchiveInstalledStartupPreservesRecordedFacts(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	h := exerciseWorkArchiveHistory(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	// This ticket changes committing commands, not the installed schema. The
	// installation's migration replay must preserve even legacy archives whose
	// recorded status never met the new command's eligibility rules. No inferred
	// completion, archive actor, clock-out or retrospective audit is legitimate.
	legacyJob := client.Job.Create().SetCompanyID(company).SetCustomerID(h.customer).SetJobType("Legacy unfinished archive").SetDeletedAt(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)).SaveX(ctx)
	client.Project.Create().SetCompanyID(company).SetCustomerID(h.customer).SetName("Legacy manual progress").SetCompletionPercentage(73).SetDeletedAt(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)).SaveX(ctx)
	_, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Installed viewer without lifecycle grants", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "project.view", Scope: authorization.All}})
	historyCheck(t, err)
	tables := append(h.tables, "authorization_audit")
	before := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "installed replay preserves every known fact and grant", historyDurableRows(t, pool, tables), before)
	var invented int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1`, legacyJob.ID).Scan(&invented))
	if invented != 0 {
		t.Fatal("startup invented historical Job activity")
	}
}

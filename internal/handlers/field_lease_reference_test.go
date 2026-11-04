package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestFieldLeaseIndicatorProtectedReferenceBrowser(t *testing.T) {
	for _, loss := range []string{"assignment", "scope", "operational relationship"} {
		t.Run(loss, func(t *testing.T) { fieldLeaseProtectedReference(t, loss) })
	}
}

func fieldLeaseProtectedReference(t *testing.T, loss string) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Reference Customer").SaveX(ctx)
	typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Reference equipment").SaveX(ctx)
	a := f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("REFERENCE-ASSET").SaveX(ctx)
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Reference job").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	var today time.Time
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&today))
	start := today.AddDate(0, 0, 1)
	end := start.AddDate(0, 1, 0)
	svc, actor := authorization.New(f.pool), authorization.Actor{UserID: f.admin}
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start.Format(time.DateOnly), EndsOn: end.Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	zero, rent := int64(0), int64(5000)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start.Format(time.DateOnly), authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start.Format(time.DateOnly)}))
	uid, role, _ := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}})
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	token, err := f.sessions.CreateMobile(ctx, uid, "Indicator harness")
	historyCheck(t, err)
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/start" || r.URL.Path == "/__fixture/renew" || r.URL.Path == "/__fixture/terminate" {
			date := start
			if r.URL.Path != "/__fixture/start" {
				date = end.AddDate(0, 0, 1)
			}
			if r.URL.Path == "/__fixture/terminate" {
				err := svc.ChangeLeaseNotice(ctx, actor, lease.ID, authorization.LeaseNoticeInput{Action: "create", Category: "customer_early", Initiator: "customer", NoticeOn: date.Format(time.DateOnly), EffectiveOn: date.Format(time.DateOnly), Reason: "Offline termination", OperationKey: newOperationKey()})
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				date = date.AddDate(0, 0, 1)
			}
			_, err := f.pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, date.Format(time.DateOnly)))
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/revoke" {
			var err error
			switch loss {
			case "assignment":
				_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			case "scope":
				_, err = svc.SaveRole(ctx, actor, role, "Independent association reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}, 1)
			case "operational relationship":
				err = svc.ChangeAssetAssociation(ctx, actor, a.ID, authorization.AssetAssociationInput{Action: "return", EffectiveDate: today.Format(time.DateOnly), Reason: "Operational access ended", Revision: 0})
			}
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	historyCheck(t, os.Chmod(directory, 0700))
	input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "asset": fmt.Sprint(a.ID), "directory": directory})
	historyCheck(t, err)
	runCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/lease-indicator-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("lease reference: %s", out)
	historyCheck(t, err)
}

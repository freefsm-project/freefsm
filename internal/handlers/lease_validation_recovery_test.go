package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/html"
)

type leaseRecoveryFixture struct {
	ctx                                                                      context.Context
	pool                                                                     *pgxpool.Pool
	client                                                                   *ent.Client
	web                                                                      http.Handler
	cookie                                                                   *http.Cookie
	company, admin, user, role, id, customer, asset, oldFile, newFile, field int64
	path                                                                     string
	grants                                                                   []authorization.Grant
}

func newLeaseRecoveryFixture(t *testing.T, initial bool) *leaseRecoveryFixture {
	t.Helper()
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	f := &leaseRecoveryFixture{ctx: ctx, pool: pool, client: client, web: web, company: company, admin: admin}
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "customer.view", "asset.view", "file.upload", "file.download"} {
		f.grants = append(f.grants, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	svc := authorization.New(pool)
	var err error
	f.role, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease correction editor", f.grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Lease correction editor").SetEmail("lease-correction65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	f.user = u.ID
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, f.role))
	f.cookie = sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery Customer").SaveX(ctx)
	f.customer = c.ID
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended recovery Asset").SaveX(ctx)
	f.asset = a.ID
	fieldPost(t, web, f.cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&f.id))
	f.path = fmt.Sprintf("/leases/%d", f.id)
	upload := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("object_type", "lease")
		r.URL.RawQuery = q.Encode()
		web.ServeHTTP(w, r)
	})
	for _, name := range []string{"original-agreement.txt", "replacement-agreement.txt"} {
		response := uploadFileHTTP(t, upload, f.cookie, f.id, name, []byte("Externally supplied agreement"))
		if response.Code != 200 {
			t.Fatalf("upload %d %s", response.Code, response.Body.String())
		}
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT min(id),max(id) FROM files`).Scan(&f.oldFile, &f.newFile))
	values := url.Values{"revision": {"0"}, "title": {"Persisted title"}, "asset_ids": {fmt.Sprint(a.ID)}}
	if initial {
		values.Set("agreement_file_id", fmt.Sprint(f.oldFile))
	}
	fieldPost(t, web, f.cookie, f.path, values, 303)
	field := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Required correction answer").SetFieldType("text").SetRequired(true).SaveX(ctx)
	f.field = field.ID
	return f
}

func (f *leaseRecoveryFixture) receipt(t *testing.T, id string) saveaction.Receipt {
	t.Helper()
	var receipt saveaction.Receipt
	historyCheck(t, json.Unmarshal([]byte(requestBody(t, f.web, f.cookie, "GET", "/save-actions/"+id, 200)), &receipt))
	return receipt
}

func agreementFormSelection(t *testing.T, body string) (string, bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	historyCheck(t, err)
	var selectNode *html.Node
	walkSaveNodes(doc, func(n *html.Node) {
		if n.Data == "select" && saveAttr(n, "name") == "agreement_file_id" {
			selectNode = n
		}
	})
	if selectNode == nil {
		return "", false
	}
	value := ""
	first := true
	walkSaveNodes(selectNode, func(n *html.Node) {
		if n.Data == "option" {
			if first {
				value = saveAttr(n, "value")
				first = false
			}
			if hasSaveAttr(n, "selected") {
				value = saveAttr(n, "value")
			}
		}
	})
	return value, true
}

func TestLeaseAgreementValidationRetainsExactIntentHTTP(t *testing.T) {
	for _, intent := range []string{"replace", "clear", "omitted", "select-from-empty"} {
		t.Run(intent, func(t *testing.T) {
			f := newLeaseRecoveryFixture(t, intent != "select-from-empty")
			values := url.Values{"revision": {"1"}, "title": {"Retained attempted title"}, "asset_ids": {fmt.Sprint(f.asset)}, "save_action_id": {newOperationKey()}}
			want := fmt.Sprint(f.oldFile)
			switch intent {
			case "replace", "select-from-empty":
				want = fmt.Sprint(f.newFile)
				values.Set("agreement_file_id", want)
			case "clear":
				want = ""
				values.Set("agreement_file_id", "")
			}
			before := leaseDurableState(t, f.pool)
			op := values.Get("save_action_id")
			response := fieldPost(t, f.web, f.cookie, f.path, values, 422)
			if selected, found := agreementFormSelection(t, response.Body.String()); !found || selected != want {
				t.Fatalf("%s server recovery selection=%q present=%v; want %q", intent, selected, found, want)
			}
			historyEqual(t, "invalid details leave all persisted facts and audit unchanged", leaseDurableState(t, f.pool), before)
			receipt := f.receipt(t, op)
			if receipt.Disposition != "rejected" || !receipt.RetrySafe || receipt.CommittedSteps != 0 {
				t.Fatalf("validation receipt: %+v", receipt)
			}
			values.Set(fmt.Sprintf("custom_field_%d", f.field), "Reviewed answer")
			conflict := fieldPost(t, f.web, f.cookie, f.path, values, 409)
			assertContains(t, conflict.Body.String(), "identity_conflict")
			historyEqual(t, "changing rejected intent cannot reuse its identity", leaseDurableState(t, f.pool), before)
			corrected := newOperationKey()
			values.Set("save_action_id", corrected)
			fieldPost(t, f.web, f.cookie, f.path, values, 303)
			var selected *int64
			var revision, changes int64
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT agreement_file_id,details_revision FROM leases WHERE id=$1`, f.id).Scan(&selected, &revision))
			actual := ""
			if selected != nil {
				actual = fmt.Sprint(*selected)
			}
			if actual != want || revision != 2 {
				t.Fatalf("corrected effect selection=%q revision=%d want %q/2", actual, revision, want)
			}
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM authorization_audit WHERE action='lease.details.change' AND target_id=$1`, f.id).Scan(&changes))
			if changes != 2 {
				t.Fatalf("expected setup plus one correction, got %d", changes)
			}
			if got := f.receipt(t, corrected); got.Disposition != "saved" {
				t.Fatalf("correction receipt: %+v", got)
			}
			after := leaseDurableState(t, f.pool)
			fieldPost(t, f.web, f.cookie, f.path, values, 409)
			historyEqual(t, "replay does not repeat correction", leaseDurableState(t, f.pool), after)
			if got := f.receipt(t, op); got.Disposition != "rejected" {
				t.Fatal("correction rewrote original receipt")
			}
		})
	}
}

func TestLeasePrewriteValidationReceiptsHTTP(t *testing.T) {
	f := newLeaseRecoveryFixture(t, true)
	archived := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Not a selectable Customer").SaveX(f.ctx)
	_, err := f.pool.Exec(f.ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, archived.ID)
	historyCheck(t, err)
	cases := []struct {
		name, path string
		values     url.Values
		status     int
	}{
		{"registration", "/leases", url.Values{"customer_id": {"bad"}}, 400},
		{"base revision", f.path, url.Values{"revision": {"bad"}}, 409},
		{"base Asset", f.path, url.Values{"revision": {"1"}, "asset_ids": {"bad"}}, 400},
		{"base premises", f.path, url.Values{"revision": {"1"}, "premises_id": {"bad"}}, 409},
		{"base agreement", f.path, url.Values{"revision": {"1"}, "agreement_file_id": {"bad"}}, 409},
		{"billing revision", f.path + "/billing", url.Values{"revision": {"bad"}}, 409},
		{"billing Asset", f.path + "/billing", url.Values{"revision": {"0"}, "asset_id": {"bad"}}, 400},
		{"billing initial", f.path + "/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(f.asset)}, "initial_cents": {"bad"}}, 400},
		{"billing rent", f.path + "/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(f.asset)}, "rent_cents": {"-1"}}, 400},
		{"billing lifecycle", f.path + "/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(f.asset)}, "lifecycle_revision": {"bad"}}, 409},
		{"registration domain preflight", "/leases", url.Values{"customer_id": {fmt.Sprint(archived.ID)}}, 409},
		{"base domain revision", f.path, url.Values{"revision": {"0"}}, 409},
		{"billing domain tax", f.path + "/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(f.asset)}, "tax_rate": {"101"}}, 409},
		{"billing hidden Asset", f.path + "/billing", url.Values{"revision": {"0"}, "asset_id": {"999999"}}, 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := leaseDurableState(t, f.pool)
			op := newOperationKey()
			tc.values.Set("save_action_id", op)
			fieldPost(t, f.web, f.cookie, tc.path, tc.values, tc.status)
			receipt := f.receipt(t, op)
			if receipt.Disposition != "rejected" || !receipt.RetrySafe || receipt.CommittedSteps != 0 {
				t.Fatalf("known pre-write failure: %+v", receipt)
			}
			historyEqual(t, "prewrite failure cannot alter domain state", leaseDurableState(t, f.pool), before)
		})
	}
}

func TestLeaseAgreementRecoveryRefiltersCurrentAuthorityHTTP(t *testing.T) {
	for _, change := range []string{"download-replace", "download-clear", "download-omitted", "removed-file", "wrong-parent", "base-view", "base-manage"} {
		t.Run(change, func(t *testing.T) {
			f := newLeaseRecoveryFixture(t, true)
			assertContains(t, requestBody(t, f.web, f.cookie, "GET", f.path, 200), "replacement-agreement.txt")
			values := url.Values{"revision": {"1"}, "title": {"Private retained input"}, "agreement_file_id": {fmt.Sprint(f.newFile)}, "save_action_id": {newOperationKey()}}
			want := 403
			switch change {
			case "removed-file":
				_, err := f.pool.Exec(f.ctx, `UPDATE files SET removed_at=clock_timestamp(),removed_by=$2 WHERE id=$1`, f.newFile, f.admin)
				historyCheck(t, err)
				want = 404
			case "wrong-parent":
				fieldPost(t, f.web, f.cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(f.customer)}}, 303)
				var other int64
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT max(id) FROM leases`).Scan(&other))
				upload := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					q := r.URL.Query()
					q.Set("object_type", "lease")
					r.URL.RawQuery = q.Encode()
					f.web.ServeHTTP(w, r)
				})
				if response := uploadFileHTTP(t, upload, f.cookie, other, "other-parent-private.txt", []byte("Other agreement")); response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				var otherFile int64
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT max(id) FROM files`).Scan(&otherFile))
				values.Set("agreement_file_id", fmt.Sprint(otherFile))
				want = 404
			default:
				grants := []authorization.Grant{}
				for _, g := range f.grants {
					if strings.HasPrefix(change, "download-") && g.Permission == "file.download" || change == "base-manage" && g.Permission == "lease.manage" {
						continue
					}
					if change == "base-view" && (g.Permission == "lease.view" || g.Permission == "lease.manage") {
						continue
					}
					grants = append(grants, g)
				}
				_, err := authorization.New(f.pool).SaveRole(f.ctx, authorization.Actor{UserID: f.admin}, f.role, "Lease correction editor", grants, 1)
				historyCheck(t, err)
				if change == "download-clear" {
					values.Set("agreement_file_id", "")
				}
				if change == "download-omitted" {
					values.Del("agreement_file_id")
					want = 422
				}
			}
			before := leaseDurableState(t, f.pool)
			response := fieldPost(t, f.web, f.cookie, f.path, values, want)
			if _, present := agreementFormSelection(t, response.Body.String()); present {
				t.Fatal("unavailable agreement intent was replaced by an executable fallback")
			}
			for _, private := range []string{"original-agreement.txt", "replacement-agreement.txt", "other-parent-private.txt"} {
				if want != 422 && strings.Contains(response.Body.String(), private) {
					t.Fatal("recovery denial leaked File metadata")
				}
			}
			historyEqual(t, "rejected current-authority correction has no effect", leaseDurableState(t, f.pool), before)
			if receipt := f.receipt(t, values.Get("save_action_id")); receipt.Disposition != "rejected" || !receipt.RetrySafe {
				t.Fatalf("prewrite authority rejection: %+v", receipt)
			}
			if change == "download-omitted" {
				values.Set(fmt.Sprintf("custom_field_%d", f.field), "Permitted unrelated correction")
				values.Set("save_action_id", newOperationKey())
				fieldPost(t, f.web, f.cookie, f.path, values, 303)
				var selected int64
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT agreement_file_id FROM leases WHERE id=$1`, f.id).Scan(&selected))
				if selected != f.oldFile {
					t.Fatal("omitted inaccessible agreement was cleared")
				}
			}
		})
	}
}

func TestLeaseDispatchedAuditFailuresStayUncertainHTTP(t *testing.T) {
	for _, kind := range []string{"registration", "base", "billing", "commit"} {
		t.Run(kind, func(t *testing.T) {
			f := newLeaseRecoveryFixture(t, true)
			path := f.path
			action := "lease.details.change"
			values := url.Values{"revision": {"1"}, "agreement_file_id": {fmt.Sprint(f.newFile)}, fmt.Sprintf("custom_field_%d", f.field): {"Reviewed valid input"}}
			if kind == "registration" {
				path = "/leases"
				action = "lease.create"
				values = url.Values{"customer_id": {fmt.Sprint(f.customer)}}
			}
			if kind == "billing" {
				path = f.path + "/billing"
				action = "lease.billing.change"
				values = url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(f.asset)}, "rent_cents": {"12345"}}
			}
			_, err := f.pool.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION reject_lease_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'injected dispatched audit uncertainty'; END IF; RETURN NEW; END $$`, action))
			historyCheck(t, err)
			trigger := `CREATE TRIGGER reject_lease_recovery_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_lease_recovery_audit()`
			if kind == "commit" {
				trigger = `CREATE CONSTRAINT TRIGGER reject_lease_recovery_audit AFTER INSERT ON authorization_audit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_lease_recovery_audit()`
			}
			_, err = f.pool.Exec(f.ctx, trigger)
			historyCheck(t, err)
			before := leaseDurableState(t, f.pool)
			op := newOperationKey()
			values.Set("save_action_id", op)
			fieldPost(t, f.web, f.cookie, path, values, 409)
			receipt := f.receipt(t, op)
			if receipt.Disposition != "review" || receipt.RetrySafe || receipt.CommittedSteps != 0 {
				t.Fatalf("dispatched failure falsely confirmed rejected: %+v", receipt)
			}
			historyEqual(t, "observed rollback retains old state without guessing receipt certainty", leaseDurableState(t, f.pool), before)
			fieldPost(t, f.web, f.cookie, path, values, 409)
			historyEqual(t, "uncertain identity never redispatches", leaseDurableState(t, f.pool), before)
			_, err = f.pool.Exec(f.ctx, `DROP TRIGGER reject_lease_recovery_audit ON authorization_audit`)
			historyCheck(t, err)
			current := f.path
			if kind == "billing" {
				current += "/billing"
			}
			requestBody(t, f.web, f.cookie, "GET", current, 200)
			corrected := newOperationKey()
			values.Set("save_action_id", corrected)
			fieldPost(t, f.web, f.cookie, path, values, 303)
			if got := f.receipt(t, corrected); got.Disposition != "saved" {
				t.Fatalf("reviewed new intent: %+v", got)
			}
			after := leaseDurableState(t, f.pool)
			fieldPost(t, f.web, f.cookie, path, values, 409)
			historyEqual(t, "reviewed correction commits once", leaseDurableState(t, f.pool), after)
			if got := f.receipt(t, op); got.Disposition != "review" || got.RetrySafe {
				t.Fatal("later correction fabricated certainty for old action")
			}
		})
	}
}

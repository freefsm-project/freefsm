package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestDiscoveryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	uid, role, cookie := f.reader(t, "Browser106", authorization.Grant{Permission: "item.view", Scope: authorization.All}, authorization.Grant{Permission: "item.create", Scope: authorization.All}, authorization.Grant{Permission: "contact.view", Scope: authorization.All}, authorization.Grant{Permission: "location.view", Scope: authorization.All}, authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	_, err := f.pool.Exec(ctx, `UPDATE users SET password_hash=(SELECT password_hash FROM users WHERE id=$1) WHERE id=$2`, f.admin, uid)
	historyCheck(t, err)
	var first int64
	var itemIDs []int64
	for i := 0; i < 80; i++ {
		item := f.client.Item.Create().SetCompanyID(f.company).SetName(fmt.Sprintf("Browser106 %03d", i)).SaveX(ctx)
		itemIDs = append(itemIDs, item.ID)
		if i == 25 {
			first = item.ID
		}
	}
	f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Separate106 customer").SaveX(ctx)
	customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Lease browser owner").SaveX(ctx)
	children := map[string][]string{"contacts": {}, "locations": {}}
	for i := 0; i < 31; i++ {
		name := fmt.Sprintf("ChildBrowser106 %03d", i)
		contact := f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetFirstName(name).SaveX(ctx)
		location := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle(name).SaveX(ctx)
		children["contacts"] = append(children["contacts"], fmt.Sprintf("/contacts/%d", contact.ID))
		children["locations"] = append(children["locations"], fmt.Sprintf("/locations/%d", location.ID))
	}
	childJSON, err := json.Marshal(children)
	historyCheck(t, err)
	lease, err := authorization.New(f.pool).RegisterDraftLease(ctx, authorization.Actor{UserID: f.admin}, customer.ID)
	historyCheck(t, err)
	historyCheck(t, authorization.New(f.pool).SaveDraftLeaseDetails(ctx, authorization.Actor{UserID: f.admin}, lease.ID, authorization.DraftLeaseDetailsInput{Title: "BrowserLease106"}))
	_, _, other := f.reader(t, "BrowserOther106", authorization.Grant{Permission: "item.view", Scope: authorization.All})
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture/items-visibility", func(w http.ResponseWriter, r *http.Request) {
		for _, id := range itemIDs {
			if err := authorization.New(f.pool).SetItemArchived(r.Context(), authorization.Actor{UserID: f.admin}, id, r.URL.Query().Get("archived") == "1"); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/fixture/hidden-item", func(w http.ResponseWriter, r *http.Request) {
		item, err := f.client.Item.Create().SetCompanyID(f.company).SetName("NoMatchBrowser106 hidden").Save(r.Context())
		if err == nil {
			err = authorization.New(f.pool).SetItemArchived(r.Context(), authorization.Actor{UserID: f.admin}, item.ID, true)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.HandleFunc("/fixture/change", func(w http.ResponseWriter, r *http.Request) {
		_, err := f.pool.Exec(r.Context(), `UPDATE items SET description='Fresh persisted description106' WHERE id=$1`, first)
		if err != nil {
			http.Error(w, "fixture failure", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/fixture/revoke", func(w http.ResponseWriter, r *http.Request) {
		_, err := authorization.New(f.pool).SaveRole(r.Context(), authorization.Actor{UserID: f.admin}, role, "Browser106", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}, 1)
		if err != nil {
			http.Error(w, "fixture failure", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/fixture/expire", func(w http.ResponseWriter, r *http.Request) {
		if err := f.sessions.Delete(r.Context(), cookie.Value); err != nil {
			http.Error(w, "fixture failure", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(f.web)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	out := os.Getenv("E")
	if out == "" {
		t.Fatal("E must name durable issue106 evidence root")
	}
	artifacts := filepath.Join(out, "browser-artifacts", fmt.Sprintf("acceptance-discovery-%d", time.Now().UnixNano()))
	historyCheck(t, os.MkdirAll(artifacts, 0700))
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(browserCtx, "node", "testdata/browser/discovery-persisted.cjs", server.URL, cookie.Value, other.Value, fmt.Sprint(first), t.TempDir(), artifacts, string(childJSON))
	output, err := cmd.CombinedOutput()
	historyCheck(t, os.WriteFile(filepath.Join(artifacts, "browser.log"), output, 0600))
	t.Logf("browser artifacts: %s\n%s", artifacts, output)
	historyCheck(t, err)
	state, err := authorization.New(f.pool).LoadListState(ctx, authorization.Actor{UserID: uid}, "search")
	historyCheck(t, err)
	if state.Revision < 3 {
		t.Fatalf("browser never durably saved/reset filters: %+v", state)
	}
	for _, key := range []string{"customers", "items", "leases", "contacts", "locations"} {
		state, err := authorization.New(f.pool).LoadListState(ctx, authorization.Actor{UserID: uid}, key)
		historyCheck(t, err)
		if state.Revision < 2 {
			t.Fatalf("browser did not save/reset %s: %+v", key, state)
		}
	}
}

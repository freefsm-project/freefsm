package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assetAdapterSnapshot(t *testing.T, pool *pgxpool.Pool, user int64, d exports.Definition, options string) []exports.Record {
	t.Helper()
	result := []exports.Record{}
	err := authorization.New(pool).WithinSnapshotPermission(context.Background(), authorization.Actor{UserID: user}, d.Permission(), d.Scope(), func(tx pgx.Tx, p authorization.Principal) error {
		return d.Snapshot(context.Background(), tx, p, json.RawMessage(options), func(r exports.Record) error { result = append(result, r); return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func assetAdapterRecheck(pool *pgxpool.Pool, user int64, d exports.Definition, ref json.RawMessage) error {
	return authorization.New(pool).WithinActor(context.Background(), authorization.Actor{UserID: user}, func(tx pgx.Tx, p authorization.Principal) error { return d.Recheck(context.Background(), tx, p, ref) })
}
func assetAdapterValues(d exports.Definition, r exports.Record) map[string]string {
	out := map[string]string{}
	for i, k := range d.Columns() {
		out[k] = r.Values[i]
	}
	return out
}

func assetAdapterUpdateRole(t *testing.T, pool *pgxpool.Pool, actor authorization.Actor, role int64, name string, grants []authorization.Grant) {
	t.Helper()
	var revision int64
	historyCheck(t, pool.QueryRow(context.Background(), `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	_, err := authorization.New(pool).SaveRole(context.Background(), actor, role, name, grants, revision)
	historyCheck(t, err)
}

// The fixture mounts handlers.New with production middleware and persisted
// browser sessions. Exercise review, acceptance, durable worker, status and
// download through that router rather than calling export handlers directly.
func assetRoutedCSV(t *testing.T, pool *pgxpool.Pool, router http.Handler, cookie *http.Cookie, route string, options url.Values, definition exports.Definition) (string, map[string]string, string) {
	t.Helper()
	requestBody(t, router, cookie, http.MethodGet, route+"?"+options.Encode(), http.StatusOK)
	options.Set("operation_id", newOperationKey())
	path := masterPost(t, router, cookie, route, options, http.StatusSeeOther)
	var status exports.Status
	masterJSON(t, router, cookie, path, &status)
	if status.State != "Pending" {
		t.Fatalf("accepted status: %+v", status)
	}
	worked, err := exports.Product(pool).ProcessOne(context.Background())
	historyCheck(t, err)
	if !worked {
		t.Fatal("production export worker found no job")
	}
	masterJSON(t, router, cookie, path, &status)
	if status.State != "Ready" || status.RowCount == nil || *status.RowCount != 1 {
		t.Fatalf("generated status: %+v", status)
	}
	var persisted int
	historyCheck(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM export_records WHERE export_id=$1 AND ordinal>0`, status.ID).Scan(&persisted))
	if persisted != 1 {
		t.Fatalf("persisted export records: %d", persisted)
	}
	body := requestBody(t, router, cookie, http.MethodGet, path+"/download", http.StatusOK)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	historyCheck(t, err)
	if len(rows) != 2 || !slices.Equal(rows[0], definition.Columns()) {
		t.Fatalf("download headers/rows: %v", rows)
	}
	values := map[string]string{}
	for i, column := range rows[0] {
		values[column] = rows[1][i]
	}
	return path, values, body
}

func TestAssetExportListFiltersAndEmptySelectionRecheck(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selected Customer").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Selected type").SaveX(ctx)
	otherType := client.AssetType.Create().SetCompanyID(company).SetName("Other type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Selected status").SaveX(ctx)
	otherStatus := client.AssetStatus.Create().SetCompanyID(company).SetName("Other status").SaveX(ctx)
	for _, fixture := range []struct {
		name                  string
		customer, typ, status int64
	}{
		{"Zulu target", c.ID, typ.ID, status.ID},
		{"Alpha target", c.ID, typ.ID, status.ID},
		{"Other owner", other.ID, typ.ID, status.ID},
		{"Other type", c.ID, otherType.ID, status.ID},
		{"Other status", c.ID, typ.ID, otherStatus.ID},
	} {
		client.Asset.Create().SetCompanyID(company).SetCustomerID(fixture.customer).SetOwnershipClass("customer_owned").SetName(fixture.name).SetAssetTypeID(fixture.typ).SetAssetStatusID(fixture.status).SaveX(ctx)
	}
	d := exports.Asset{}
	options := fmt.Sprintf(`{"customer_id":%d,"asset_type_id":%d,"asset_status_id":%d}`, c.ID, typ.ID, status.ID)
	rows := assetAdapterSnapshot(t, pool, admin, d, options)
	if len(rows) != 2 || assetAdapterValues(d, rows[0])["name"] != "Alpha target" || assetAdapterValues(d, rows[1])["name"] != "Zulu target" {
		t.Fatalf("filters/default order: %v", rows)
	}
	if err := d.Validate(json.RawMessage(`{"archived_only":true}`)); !errors.Is(err, exports.ErrConflict) {
		t.Fatalf("archived-only without archived authority intent accepted: %v", err)
	}
	archivedID := assetAdapterValues(d, rows[0])["asset_id"]
	_, err := pool.Exec(ctx, `UPDATE assets SET deleted_at=clock_timestamp() WHERE id=$1::text::bigint`, archivedID)
	historyCheck(t, err)
	archiveOptions := fmt.Sprintf(`{"customer_id":%d,"asset_type_id":%d,"asset_status_id":%d,"archived":true,"archived_only":true}`, c.ID, typ.ID, status.ID)
	archivedRows := assetAdapterSnapshot(t, pool, admin, d, archiveOptions)
	if len(archivedRows) != 1 || assetAdapterValues(d, archivedRows[0])["asset_id"] != archivedID {
		t.Fatalf("archived-only included active Assets or lost selected active Customer context: %v", archivedRows)
	}
	inclusiveOptions := strings.Replace(archiveOptions, `"archived_only":true`, `"archived_only":false`, 1)
	if rows := assetAdapterSnapshot(t, pool, admin, d, inclusiveOptions); len(rows) != 2 {
		t.Fatalf("archived inclusive filter lost active or archived Assets: %v", rows)
	}
	if rows := assetAdapterSnapshot(t, pool, admin, d, options); len(rows) != 1 || assetAdapterValues(d, rows[0])["name"] != "Zulu target" {
		t.Fatalf("active default included archived Asset: %v", rows)
	}
	for _, key := range []string{"customer_id", "asset_type_id", "asset_status_id"} {
		if err := d.Validate(json.RawMessage(fmt.Sprintf(`{"%s":-1}`, key))); !errors.Is(err, exports.ErrConflict) {
			t.Fatalf("negative %s accepted: %v", key, err)
		}
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("asset-filter-export@test").SetName("Exporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	base := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.export", Scope: authorization.All}}
	grants := append(append([]authorization.Grant{}, base...), authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	role, err := auth.SaveRole(ctx, actor, 0, "Asset filter exporter", grants)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
	service := exports.New(pool, d)
	empty := json.RawMessage(fmt.Sprintf(`{"customer_id":%d,"search":"no matching equipment"}`, c.ID))
	exportID, err := service.Accept(ctx, authorization.Actor{UserID: user.ID}, newOperationKey(), d.Kind(), empty)
	historyCheck(t, err)
	worked, err := service.ProcessOne(ctx)
	historyCheck(t, err)
	if !worked {
		t.Fatal("export worker did not run")
	}
	receipt, err := service.Status(ctx, authorization.Actor{UserID: user.ID}, exportID)
	historyCheck(t, err)
	if receipt.State != "Ready" || receipt.RowCount == nil || *receipt.RowCount != 0 || receipt.Blocked {
		t.Fatalf("empty export: %+v", receipt)
	}
	assetAdapterUpdateRole(t, pool, actor, role, "Asset filter exporter", base)
	receipt, err = service.Status(ctx, authorization.Actor{UserID: user.ID}, exportID)
	historyCheck(t, err)
	if !receipt.Blocked || receipt.RowCount != nil {
		t.Fatalf("empty selection lost authority not blocked: %+v", receipt)
	}
	_, err = service.Accept(ctx, authorization.Actor{UserID: user.ID}, newOperationKey(), d.Kind(), empty)
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("unauthorized Customer filter accepted: %v", err)
	}
	// Recheck the selected identity as well as its grant: even an empty selection
	// cannot keep disclosing an archived Customer without archived authority.
	selected := client.Customer.Create().SetCompanyID(company).SetDisplayName("Empty selected Customer").SaveX(ctx)
	assetAdapterUpdateRole(t, pool, actor, role, "Asset filter exporter", grants)
	empty = json.RawMessage(fmt.Sprintf(`{"customer_id":%d}`, selected.ID))
	exportID, err = service.Accept(ctx, authorization.Actor{UserID: user.ID}, newOperationKey(), d.Kind(), empty)
	historyCheck(t, err)
	worked, err = service.ProcessOne(ctx)
	historyCheck(t, err)
	if !worked {
		t.Fatal("export worker did not run")
	}
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, selected.ID)
	historyCheck(t, err)
	download, err := service.PrepareDownload(ctx, authorization.Actor{UserID: user.ID}, exportID)
	if download != nil {
		_ = download.Close()
		t.Fatal("archived selected Customer permitted empty download")
	}
	if !errors.Is(err, exports.ErrUnavailable) {
		t.Fatalf("archived selection download: %v", err)
	}
	receipt, err = service.Status(ctx, authorization.Actor{UserID: user.ID}, exportID)
	historyCheck(t, err)
	if !receipt.Blocked {
		t.Fatal("archived selected Customer did not block empty export")
	}
}

func TestAssetExportBoundedAssociationsAndDisclosureRevocationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE owner label").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("PRIVATE location label").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Retained type").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetOwnershipClass("company_owned").SetName("Placed pump").SetAssetTypeID(typ.ID).SetCustomFields(`[{"field_id":42,"value":"Retained value"}]`).SaveX(ctx)
	user := client.User.Create().SetCompanyID(company).SetEmail("asset-export@test").SetName("Exporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	base := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.export", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Asset exporter", base)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
	_, err = pool.Exec(ctx, `INSERT INTO asset_association_origin_revisions(asset_id,effective_date,customer_id,location_id,reason,actor_id,before_fact) VALUES($1,current_date,$2,$3,'Reviewed observation',$4,'{"private_reference":"NEVER RAW"}')`, a.ID, c.ID, l.ID, admin)
	historyCheck(t, err)
	d := exports.Asset{}
	rows := assetAdapterSnapshot(t, pool, user.ID, d, `{"sort":"number"}`)
	if len(rows) != 1 {
		t.Fatalf("rows: %d", len(rows))
	}
	restricted := rows[0]
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	restrictedPath, v, body := assetRoutedCSV(t, pool, router, cookie, "/assets/export", url.Values{"sort": {"number"}}, d)
	if v["association_kind"] != "placement" || v["customer_id"] != "" || v["location_id"] != "" {
		t.Fatalf("bounded placement: %v", v)
	}
	all := body
	if strings.Contains(all, "PRIVATE") || strings.Contains(all, "NEVER RAW") {
		t.Fatal("restricted association leaked")
	}
	assertContains(t, v["association_periods"], `"starts_on":null`)
	assertContains(t, v["association_events"], `"event_id":0`)
	var periods []struct {
		EventID  int64   `json:"event_id"`
		StartsOn *string `json:"starts_on"`
	}
	historyCheck(t, json.Unmarshal([]byte(v["association_periods"]), &periods))
	if len(periods) != 1 || periods[0].EventID != 0 || periods[0].StartsOn != nil {
		t.Fatalf("invented origin date: %s", v["association_periods"])
	}
	var events []struct {
		EventID    int64     `json:"event_id"`
		Action     string    `json:"action"`
		Reason     string    `json:"reason"`
		RecordedAt time.Time `json:"recorded_at"`
	}
	historyCheck(t, json.Unmarshal([]byte(v["association_events"]), &events))
	if len(events) != 1 || events[0].EventID != 0 || events[0].Action != "correct" || events[0].Reason != "Reviewed observation" || events[0].RecordedAt.IsZero() {
		t.Fatalf("origin correction provenance: %s", v["association_events"])
	}
	if v["ownership_class"] != "company_owned" || v["asset_id"] != fmt.Sprint(a.ID) {
		t.Fatalf("Asset identity/class: %v", v)
	}
	assertContains(t, all, "Retained value")
	assertContains(t, all, "Reviewed observation")
	withCustomer := append(append([]authorization.Grant{}, base...), authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	assetAdapterUpdateRole(t, pool, actor, role, "Asset exporter", withCustomer)
	rows = assetAdapterSnapshot(t, pool, user.ID, d, `{}`)
	visible := rows[0]
	visiblePath, v, _ := assetRoutedCSV(t, pool, router, cookie, "/assets/export", url.Values{}, d)
	if v["customer_id"] != fmt.Sprint(c.ID) || v["location_id"] != "" {
		t.Fatalf("independent facets: %v", v)
	}
	assetAdapterUpdateRole(t, pool, actor, role, "Asset exporter", base)
	if err := assetAdapterRecheck(pool, user.ID, d, visible.Reference); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("disclosed authority loss: %v", err)
	}
	historyCheck(t, assetAdapterRecheck(pool, user.ID, d, restricted.Reference))
	expectStatus(t, router, cookie, http.MethodGet, visiblePath+"/download", http.StatusGone)
	expectStatus(t, router, cookie, http.MethodGet, restrictedPath+"/download", http.StatusOK)
}

func TestServiceEntryExportLifetimeRevisionsAndAssetRecheckHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE service customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Bounded archived Job title").SaveX(ctx)
	person := client.User.Create().SetCompanyID(company).SetEmail("service-person@test").SetName("Old name").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	user := client.User.Create().SetCompanyID(company).SetEmail("service-export@test").SetName("Exporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "service_entry.view", Scope: authorization.All}, {Permission: "service_entry.export", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Lifetime exporter", grants)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
	var entry int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO asset_service_entries(company_id,asset_id,job_id,link_id,customer_id,association_event_id,actor_id) SELECT $1,$2,$3,id,$4,association_event_id,$5 FROM job_asset_links WHERE job_id=$3 RETURNING id`, company, a.ID, j.ID, c.ID, admin).Scan(&entry))
	first, second := "97000000-0000-4000-8000-000000000001", "97000000-0000-4000-8000-000000000002"
	_, err = pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id) VALUES($1,$2,current_date-1,'Original summary',$3)`, first, entry, admin)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id,predecessor) VALUES($1,$2,current_date,'Current summary',$3,$4)`, second, entry, admin, first)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO asset_service_responsible_users(revision_id,user_id) VALUES($1,$3),($2,$3)`, first, second, person.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE users SET name='Renamed disabled participant',is_active=false WHERE id=$1`, person.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE job_asset_links SET operational=false,unlinked_at=clock_timestamp() WHERE job_id=$1`, j.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID)
	historyCheck(t, err)
	d := exports.ServiceEntry{}
	rows := assetAdapterSnapshot(t, pool, user.ID, d, `{"sort":"date_desc"}`)
	if len(rows) != 1 {
		t.Fatalf("lifetime rows: %d", len(rows))
	}
	record := rows[0]
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	path, v, body := assetRoutedCSV(t, pool, router, cookie, "/service-entries/export", url.Values{"sort": {"date_desc"}, "asset_id": {fmt.Sprint(a.ID)}}, d)
	if v["summary"] != "Current summary" || v["job_id"] != fmt.Sprint(j.ID) || v["revision_id"] != second {
		t.Fatalf("current evidence: %v", v)
	}
	assertContains(t, v["revisions"], "Original summary")
	assertContains(t, v["revisions"], `"predecessor":"`+first+`"`)
	assertContains(t, v["revisions"], "Renamed disabled participant")
	assertContains(t, v["responsible_users"], fmt.Sprint(person.ID))
	assertNotContains(t, body, "PRIVATE")
	assertContains(t, v["job_title"], "Bounded archived Job title")
	var history []struct {
		ID               string  `json:"id"`
		Predecessor      *string `json:"predecessor"`
		ServiceDate      string  `json:"service_date"`
		Summary          string  `json:"summary"`
		ResponsibleUsers []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"responsible_users"`
		Actor struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"actor"`
		AcceptedAt time.Time `json:"accepted_at"`
	}
	historyCheck(t, json.Unmarshal([]byte(v["revisions"]), &history))
	if len(history) != 2 || history[0].ID != first || history[0].Predecessor != nil || history[1].ID != second || history[1].Predecessor == nil || *history[1].Predecessor != first {
		t.Fatalf("revision chain: %s", v["revisions"])
	}
	for _, revision := range history {
		if revision.Actor.ID != admin || revision.Actor.Name != "Admin" || revision.AcceptedAt.IsZero() || revision.ServiceDate == "" || len(revision.ResponsibleUsers) != 1 || revision.ResponsibleUsers[0].ID != person.ID || revision.ResponsibleUsers[0].Name != "Renamed disabled participant" {
			t.Fatalf("revision provenance: %+v", revision)
		}
	}
	assertNotContains(t, v["revisions"], `"reason"`)
	historyCheck(t, assetAdapterRecheck(pool, user.ID, d, record.Reference))
	_, err = pool.Exec(ctx, `UPDATE assets SET deleted_at=clock_timestamp() WHERE id=$1`, a.ID)
	historyCheck(t, err)
	if err := assetAdapterRecheck(pool, user.ID, d, record.Reference); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("Asset archive must block: %v", err)
	}
	expectStatus(t, router, cookie, http.MethodGet, path+"/download", http.StatusGone)
	if rows := assetAdapterSnapshot(t, pool, user.ID, d, `{}`); len(rows) != 0 {
		t.Fatal("default included archived Asset")
	}
	grants = append(grants, authorization.Grant{Permission: "asset.archived.view", Scope: authorization.All})
	assetAdapterUpdateRole(t, pool, actor, role, "Lifetime exporter", grants)
	rows = assetAdapterSnapshot(t, pool, user.ID, d, `{"archived":true}`)
	if len(rows) != 1 {
		t.Fatal("explicit archive missing")
	}
	historyCheck(t, assetAdapterRecheck(pool, user.ID, d, record.Reference))
	// Simulate a persisted scope downgrade; effective permissions must not let
	// the retained Export grant turn Assigned history into lifetime authority.
	_, err = pool.Exec(ctx, `UPDATE authorization_grants SET scope='J' WHERE role_id=$1 AND permission='service_entry.view'`, role)
	historyCheck(t, err)
	if err := assetAdapterRecheck(pool, user.ID, d, record.Reference); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("Assigned cannot export lifetime: %v", err)
	}
}

func TestAssetExportTransferCorrectionArchivedLabelsAndHistoricalJobHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	old := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archived former owner").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE destination Customer").SaveX(ctx)
	oldLocation := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(old.ID).SetTitle("Archived former site").SaveX(ctx)
	nextLocation := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(next.ID).SetTitle("PRIVATE initially recorded site").SaveX(ctx)
	correctedLocation := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(next.ID).SetTitle("PRIVATE corrected site").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Transfer equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(old.ID).SetLocationID(oldLocation.ID).SetOwnershipClass("customer_owned").SetName("Original Job equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(old.ID).SetAssetID(a.ID).SetJobType("Historical service Job").SaveX(ctx)
	field := client.User.Create().SetCompanyID(company).SetEmail("export-historical-field@test").SetName("Assigned viewer").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	fieldRole, err := auth.SaveRole(ctx, actor, 0, "Historical Job viewer", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, field.ID, fieldRole))
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(field.ID).SaveX(ctx)
	fieldCookie := sessionCookie(t, ctx, sessions, field.ID)
	contextPath := fmt.Sprintf("/jobs/%d/master-context", job.ID)
	assertContains(t, requestBody(t, router, fieldCookie, http.MethodGet, contextPath, http.StatusOK), a.Name)
	var originalLink string
	const linkEvidence = `SELECT jsonb_build_object('id',id,'job_id',job_id,'asset_id',asset_id,'customer_id',customer_id,'number',number,'name',name,'linked_on',linked_on,'association_event_id',association_event_id)::text FROM job_asset_links WHERE job_id=$1`
	historyCheck(t, pool.QueryRow(ctx, linkEvidence, job.ID).Scan(&originalLink))
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$1),'UTC'))::date::text`, company).Scan(&today))
	root := fmt.Sprintf("/assets/%d/associations", a.ID)
	fieldPost(t, router, adminCookie, root+"/transfer", url.Values{"customer_id": {fmt.Sprint(next.ID)}, "location_id": {fmt.Sprint(nextLocation.ID)}, "effective_date": {today}, "reason": {"Recorded transfer"}, "revision": {"0"}}, http.StatusSeeOther)
	var eventID int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='transfer'`, a.ID).Scan(&eventID))
	fieldPost(t, router, adminCookie, root+"/correct", url.Values{"event_id": {fmt.Sprint(eventID)}, "customer_id": {fmt.Sprint(next.ID)}, "location_id": {fmt.Sprint(correctedLocation.ID)}, "effective_date": {today}, "reason": {"Corrected recorded site"}, "revision": {"1"}}, http.StatusSeeOther)
	var correctionID int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='correct'`, a.ID).Scan(&correctionID))
	var retainedLink string
	var operational bool
	historyCheck(t, pool.QueryRow(ctx, linkEvidence, job.ID).Scan(&retainedLink))
	historyCheck(t, pool.QueryRow(ctx, `SELECT operational FROM job_asset_links WHERE job_id=$1`, job.ID).Scan(&operational))
	if retainedLink != originalLink || operational {
		t.Fatalf("historical Job evidence changed or authorizes new placement: %s -> %s, operational=%v", originalLink, retainedLink, operational)
	}
	body := requestBody(t, router, fieldCookie, http.MethodGet, contextPath, http.StatusOK)
	assertNotContains(t, body, "PRIVATE")
	assertNotContains(t, body, a.Name)
	expectStatus(t, router, fieldCookie, http.MethodGet, root, http.StatusNotFound)
	expectStatus(t, router, fieldCookie, http.MethodGet, "/assets/export", http.StatusForbidden)
	// Retain archived references after the Job has finished contributing active
	// context. Archive fixture data does not rewrite the association evidence.
	_, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, job.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, oldLocation.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, old.ID)
	historyCheck(t, err)
	reader := client.User.Create().SetCompanyID(company).SetEmail("export-association-reader@test").SetName("Office reader").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	base := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.export", Scope: authorization.All}}
	grants := append(append([]authorization.Grant{}, base...), authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "location.view", Scope: authorization.All})
	role, err := auth.SaveRole(ctx, actor, 0, "Bounded association exporter", grants)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, reader.ID, role))
	cookie := sessionCookie(t, ctx, sessions, reader.ID)
	projection := requestBody(t, router, cookie, http.MethodGet, root, http.StatusOK)
	assertContains(t, projection, "Archived former owner (Archived)")
	assertContains(t, projection, "Archived former site (Archived)")
	_, values, _ := assetRoutedCSV(t, pool, router, cookie, "/assets/export", url.Values{}, exports.Asset{})
	type period struct {
		EventID    int64   `json:"event_id"`
		CustomerID *int64  `json:"customer_id"`
		Customer   string  `json:"customer"`
		LocationID *int64  `json:"location_id"`
		Location   string  `json:"location"`
		StartsOn   *string `json:"starts_on"`
		EndsBefore *string `json:"ends_before"`
	}
	var periods []period
	historyCheck(t, json.Unmarshal([]byte(values["association_periods"]), &periods))
	if len(periods) != 2 || periods[0].EventID != 0 || periods[0].StartsOn != nil || periods[0].EndsBefore == nil || *periods[0].EndsBefore != today {
		t.Fatalf("origin period: %s", values["association_periods"])
	}
	if periods[0].CustomerID == nil || *periods[0].CustomerID != old.ID || periods[0].LocationID == nil || *periods[0].LocationID != oldLocation.ID || periods[0].Customer != "Archived former owner (Archived)" || periods[0].Location != "Archived former site (Archived)" {
		t.Fatalf("bounded archived labels: %+v", periods[0])
	}
	if periods[1].EventID != eventID || periods[1].LocationID == nil || *periods[1].LocationID != correctedLocation.ID || periods[1].StartsOn == nil || *periods[1].StartsOn != today || periods[1].EndsBefore != nil {
		t.Fatalf("corrected period did not retain original event identity: %+v", periods[1])
	}
	var events []struct {
		ID            int64     `json:"id"`
		EventID       int64     `json:"event_id"`
		Action        string    `json:"action"`
		Reason        string    `json:"reason"`
		EffectiveDate string    `json:"effective_date"`
		RecordedAt    time.Time `json:"recorded_at"`
	}
	historyCheck(t, json.Unmarshal([]byte(values["association_events"]), &events))
	if len(events) != 2 || events[0].ID != eventID || events[0].EventID != eventID || events[0].Action != "transfer" || events[0].Reason != "Recorded transfer" || events[1].ID != correctionID || events[1].EventID != eventID || events[1].Action != "correct" || events[1].Reason != "Corrected recorded site" {
		t.Fatalf("immutable event revisions: %s", values["association_events"])
	}
	for _, event := range events {
		if event.EffectiveDate != today || event.RecordedAt.IsZero() {
			t.Fatalf("event dates: %+v", event)
		}
	}
	if values["location_id"] != fmt.Sprint(correctedLocation.ID) || values["customer_id"] != fmt.Sprint(next.ID) || values["association_kind"] != "owner" {
		t.Fatalf("current projection: %v", values)
	}
	assetAdapterUpdateRole(t, pool, actor, role, "Bounded association exporter", base)
	projection = requestBody(t, router, cookie, http.MethodGet, root, http.StatusOK)
	_, restricted, raw := assetRoutedCSV(t, pool, router, cookie, "/assets/export", url.Values{}, exports.Asset{})
	for _, label := range []string{"Archived former owner", "Archived former site", "PRIVATE"} {
		assertNotContains(t, projection, label)
		assertNotContains(t, raw, label)
	}
	if restricted["customer_id"] != "" || restricted["location_id"] != "" {
		t.Fatalf("current restricted IDs: %v", restricted)
	}
	historyCheck(t, json.Unmarshal([]byte(restricted["association_periods"]), &periods))
	for _, p := range periods {
		if p.CustomerID != nil || p.LocationID != nil {
			t.Fatalf("restricted period IDs: %+v", p)
		}
	}
	assertNotContains(t, raw, "before_fact")
	assertContains(t, restricted["association_events"], "Recorded transfer")
	assertContains(t, restricted["association_events"], "Corrected recorded site")
}

func TestAssetExportCollectionSearchParityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Search equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).
		SetNumber("EQ-FIND").SetName("Pump").SetSerialNumber("SN1").SetModel("M_1").SetManufacturer(`ACME\Tools 50%`).SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).
		SetNumber("EQ-OTHER").SetName("Control").SetSerialNumber("SN2").SetModel("MZ1").SetManufacturer("ACMEXTools 50X").SaveX(ctx)
	assetLink := regexp.MustCompile(`href="/assets/([0-9]+)"`)
	exportLink := regexp.MustCompile(`href="(/assets/export\?[^"]*)"`)
	for _, tc := range []struct {
		name, search string
		matches      bool
	}{
		{"cross-field phrase", "Pump SN1", false},
		{"name case folding", "pUmP", true},
		{"serial", "sN1", true},
		{"number", "eq-find", true},
		{"model", "m_1", true},
		{"manufacturer phrase", `acme\tools 50%`, true},
		{"literal percent", "%", true},
		{"literal underscore", "_", true},
		{"literal backslash", `\`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"search": {tc.search}}
			page := requestBody(t, router, cookie, http.MethodGet, "/assets?"+query.Encode(), http.StatusOK)
			var collectionIDs []string
			for _, match := range assetLink.FindAllStringSubmatch(page, -1) {
				collectionIDs = append(collectionIDs, match[1])
			}
			var want []string
			if tc.matches {
				want = []string{fmt.Sprint(a.ID)}
			}
			if !slices.Equal(collectionIDs, want) {
				t.Fatalf("collection search %q: IDs=%v want=%v", tc.search, collectionIDs, want)
			}
			link := exportLink.FindStringSubmatch(page)
			if len(link) != 2 {
				t.Fatal("collection omitted its Asset export link")
			}
			reviewURL, err := url.Parse(html.UnescapeString(link[1]))
			historyCheck(t, err)
			if reviewURL.Query().Get("search") != tc.search || reviewURL.Query().Get("sort") != "updated" {
				t.Fatalf("collection export intent changed: %s", reviewURL)
			}
			requestBody(t, router, cookie, http.MethodGet, reviewURL.String(), http.StatusOK)
			form := reviewURL.Query()
			form.Set("operation_id", newOperationKey())
			path := masterPost(t, router, cookie, reviewURL.Path, form, http.StatusSeeOther)
			worked, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			if !worked {
				t.Fatal("export worker did not run")
			}
			body := requestBody(t, router, cookie, http.MethodGet, path+"/download", http.StatusOK)
			rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
			historyCheck(t, err)
			if len(rows) == 0 || !slices.Equal(rows[0], (exports.Asset{}).Columns()) {
				t.Fatalf("CSV headers: %v", rows)
			}
			var exportedIDs []string
			for _, row := range rows[1:] {
				exportedIDs = append(exportedIDs, row[0])
			}
			if !slices.Equal(exportedIDs, collectionIDs) {
				t.Fatalf("collection-to-export search %q: collection IDs=%v exported IDs=%v", tc.search, collectionIDs, exportedIDs)
			}
		})
	}
}

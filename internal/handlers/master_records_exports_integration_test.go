package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func masterRecordCSV(t *testing.T, service *exports.Service, actor authorization.Actor, kind string, options exports.MasterRecordOptions) (int64, []map[string]string) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	id, err := service.Accept(ctx, actor, newOperationKey(), kind, raw)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := service.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("generation: %t %v", worked, err)
	}
	download, err := service.PrepareDownload(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Close()
	rows, err := csv.NewReader(download).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("missing header")
	}
	out := []map[string]string{}
	for _, row := range rows[1:] {
		entry := map[string]string{}
		for i, header := range rows[0] {
			entry[header] = row[i]
		}
		out = append(out, entry)
	}
	return id, out
}

func TestMasterRecordContactLocationDisclosuresPostgres(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE customer label").SaveX(ctx)
	primary := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetFirstName("Duplicate").SetEmail("same@example.test").SaveX(ctx)
	linked := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetFirstName("Duplicate").SetEmail("same@example.test").SaveX(ctx)
	primaryLocation := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Duplicate").SetAddress1("same address").SaveX(ctx)
	linkedLocation := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Duplicate").SetAddress1("same address").SaveX(ctx)
	if _, err := pool.Exec(ctx, `UPDATE customer_contacts SET is_primary=true WHERE id=$1`, primary.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE locations SET is_primary=true WHERE id=$1`, primaryLocation.ID); err != nil {
		t.Fatal(err)
	}
	var completed int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='job:completed' ORDER BY id LIMIT 1`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetCustomerContactID(linked.ID).SetLocationID(linkedLocation.ID).SetJobType("PRIVATE explicit Job").SetStatusID(completed).SaveX(ctx)
	var archivedJob int64
	if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,customer_contact_id,location_id,job_type,status_id) VALUES($1,$2,$3,$4,'PRIVATE archived Job',$5) RETURNING id`, company, customer.ID, linked.ID, linkedLocation.ID, completed).Scan(&archivedJob); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, archivedJob); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	grants := []authorization.Grant{}
	for _, resource := range []string{"contact", "location"} {
		for _, action := range []string{"view", "export", "archived.view"} {
			grants = append(grants, authorization.Grant{Permission: resource + "." + action, Scope: authorization.All})
		}
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Master record exporter", grants)
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("master-record-exporter@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: user.ID}
	service := exports.New(pool, exports.Contact{}, exports.Location{})
	options := exports.MasterRecordOptions{Sort: "name"}
	for _, d := range []exports.Definition{exports.Contact{}, exports.Location{}} {
		t.Run(d.Kind(), func(t *testing.T) {
			// The preceding case can change optional grants; restore independent
			// base authority before exercising the next adapter.
			if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission IN ('job.view','job.archived.view','customer.view')`, role); err != nil {
				t.Fatal(err)
			}
			restrictedID, rows := masterRecordCSV(t, service, actor, d.Kind(), options)
			if len(rows) != 2 {
				t.Fatalf("duplicates collapsed: %v", rows)
			}
			for _, row := range rows {
				if row["customer_id"] != "" || row["customer_label"] != "" || row["customer_visibility"] != "restricted" || row["job_links_json"] != "[]" || row["job_links_visibility"] != "restricted" {
					t.Fatalf("restricted relationship leaked: %v", row)
				}
			}
			if rows[0]["primary_customer_default"] != "true" || rows[1]["primary_customer_default"] != "false" {
				t.Fatalf("primary ordering/defaults: %v", rows)
			}
			var privateRefs bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM export_records WHERE export_id=$1 AND ordinal>0 AND (reference ? 'customer' OR jsonb_array_length(coalesce(reference->'jobs','[]'::jsonb))>0))`, restrictedID).Scan(&privateRefs); err != nil || privateRefs {
				t.Fatalf("undisclosed authority dependencies: %t %v", privateRefs, err)
			}
			selected, _ := json.Marshal(exports.MasterRecordOptions{Sort: "name", CustomerID: customer.ID})
			if _, err := service.Accept(ctx, actor, newOperationKey(), d.Kind(), selected); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("selected hidden Customer accepted: %v", err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'job.view','A')`, role); err != nil {
				t.Fatal(err)
			}
			disclosedID, rows := masterRecordCSV(t, service, actor, d.Kind(), options)
			if rows[0]["job_links_json"] != "[]" {
				t.Fatal("primary default invented a Job link")
			}
			var links []struct {
				ID       int64  `json:"id"`
				Title    string `json:"title"`
				Archived bool   `json:"archived"`
			}
			if err := json.Unmarshal([]byte(rows[1]["job_links_json"]), &links); err != nil {
				t.Fatal(err)
			}
			if len(links) != 1 || links[0].ID != job.ID || links[0].Title != "PRIVATE explicit Job" || links[0].Archived || rows[1]["job_links_visibility"] != "active_only" {
				t.Fatalf("explicit link/dedup/archive: %v %v", links, rows)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
				t.Fatal(err)
			}
			if status, err := service.Status(ctx, actor, restrictedID); err != nil || status.Blocked {
				t.Fatalf("undisclosed Job grant affected snapshot: %+v %v", status, err)
			}
			if status, err := service.Status(ctx, actor, disclosedID); err != nil || !status.Blocked {
				t.Fatalf("disclosed Job grant loss not blocked: %+v %v", status, err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'job.view','A'),($1,'job.archived.view','A')`, role); err != nil {
				t.Fatal(err)
			}
			_, activeRows := masterRecordCSV(t, service, actor, d.Kind(), options)
			if strings.Contains(activeRows[1]["job_links_json"], "PRIVATE archived Job") {
				t.Fatal("archived authority alone included archived Job")
			}
			archivedID, rows := masterRecordCSV(t, service, actor, d.Kind(), exports.MasterRecordOptions{Sort: "name", Archived: true})
			if err := json.Unmarshal([]byte(rows[1]["job_links_json"]), &links); err != nil {
				t.Fatal(err)
			}
			if len(links) != 2 || links[1].ID != archivedJob || !links[1].Archived || rows[1]["job_links_visibility"] != "visible" {
				t.Fatalf("explicit archived links: %v %v", links, rows)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.archived.view'`, role); err != nil {
				t.Fatal(err)
			}
			if _, err := service.PrepareDownload(ctx, actor, archivedID); !errors.Is(err, exports.ErrUnavailable) {
				t.Fatalf("archived Job authority loss not blocked: %v", err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'customer.view','A')`, role); err != nil {
				t.Fatal(err)
			}
			selectedID, rows := masterRecordCSV(t, service, actor, d.Kind(), exports.MasterRecordOptions{Sort: "name", CustomerID: customer.ID, Search: "matches no rows"})
			if len(rows) != 0 {
				t.Fatal("unexpected selected empty export rows")
			}
			if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, role); err != nil {
				t.Fatal(err)
			}
			if _, err := service.PrepareDownload(ctx, actor, selectedID); !errors.Is(err, exports.ErrUnavailable) {
				t.Fatalf("empty selected Customer authority loss not blocked: %v", err)
			}
		})
	}
}

func TestMasterRecordProjectSemanticsPostgres(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE project Customer").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("PRIVATE project Location").SaveX(ctx)
	var completed, pending int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='project:completed' ORDER BY id LIMIT 1`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='project:pending' ORDER BY id LIMIT 1`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	var definition int64
	if err := pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,retired,options,retired_options) VALUES($1,'project','Retired project choice','select',true,'["Old choice"]','["Old choice"]') RETURNING id`, company).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetLocationID(location.ID).SetName("Semantic project").SetStatusID(completed).SetCompletionPercentage(37).SetCustomFields(fmt.Sprintf(`[{"definition_id":%d,"value":"Old choice"}]`, definition)).SaveX(ctx)
	if _, err := pool.Exec(ctx, `UPDATE projects SET planned_start_date='2026-11-01',planned_end_date='2026-11-30' WHERE id=$1`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_legacy_review(project_id,recorded,captured_timezone,review_reasons) VALUES($1,'{"customer_id":999999,"private":"NEVER export full legacy row"}','Pacific/Auckland',ARRAY['prior non-completed manual progress is unknown; recorded 100 is not evidence'])`, project.ID); err != nil {
		t.Fatal(err)
	}
	client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Other project").SetStatusID(pending).SetCompletionPercentage(73).SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Project exporter", []authorization.Grant{{Permission: "project.view", Scope: authorization.All}, {Permission: "project.export", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("project-exporter@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: user.ID}
	service := exports.New(pool, exports.Project{})
	options := exports.MasterRecordOptions{Sort: "name", StatusID: completed}
	restrictedID, rows := masterRecordCSV(t, service, actor, (exports.Project{}).Kind(), options)
	if len(rows) != 1 {
		t.Fatalf("Status selection: %v", rows)
	}
	row := rows[0]
	for key, want := range map[string]string{"project_id": strconv.FormatInt(project.ID, 10), "status_category": "project:completed", "displayed_progress_percent": "100", "retained_manual_progress_percent": "37.00", "planned_start_date": "2026-11-01", "planned_end_date": "2026-11-30", "legacy_captured_timezone": "Pacific/Auckland", "customer_id": "", "customer_label": "", "customer_visibility": "restricted", "location_id": "", "location_label": "", "location_visibility": "restricted"} {
		if row[key] != want {
			t.Errorf("%s=%q want %q", key, row[key], want)
		}
	}
	if !strings.Contains(row["legacy_review_reasons_json"], "unknown") || strings.Contains(fmt.Sprint(row), "NEVER export") || strings.Contains(fmt.Sprint(row), "999999") {
		t.Fatalf("legacy uncertainty/leak: %v", row)
	}
	var fields struct {
		Values      []json.RawMessage `json:"values"`
		Definitions []struct {
			ID             int64    `json:"id"`
			Retired        bool     `json:"retired"`
			RetiredOptions []string `json:"retired_options"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(row["custom_fields_json"]), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields.Values) != 1 || len(fields.Definitions) != 1 || fields.Definitions[0].ID != definition || !fields.Definitions[0].Retired || len(fields.Definitions[0].RetiredOptions) != 1 {
		t.Fatalf("retired fields lost: %+v", fields)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,'customer.view','A'),($1,'location.view','A')`, role); err != nil {
		t.Fatal(err)
	}
	visibleID, visibleRows := masterRecordCSV(t, service, actor, (exports.Project{}).Kind(), options)
	if visibleRows[0]["customer_id"] != strconv.FormatInt(customer.ID, 10) || visibleRows[0]["customer_label"] != "PRIVATE project Customer" || visibleRows[0]["location_id"] != strconv.FormatInt(location.ID, 10) || visibleRows[0]["location_label"] != "PRIVATE project Location" {
		t.Fatalf("authorized relationships missing: %v", visibleRows)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='location.view'`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PrepareDownload(ctx, actor, visibleID); !errors.Is(err, exports.ErrUnavailable) {
		t.Fatalf("Location disclosure revocation: %v", err)
	}
	visibleID, _ = masterRecordCSV(t, service, actor, (exports.Project{}).Kind(), options)
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PrepareDownload(ctx, actor, visibleID); !errors.Is(err, exports.ErrUnavailable) {
		t.Fatalf("Customer disclosure revocation: %v", err)
	}
	if status, err := service.Status(ctx, actor, restrictedID); err != nil || status.Blocked {
		t.Fatalf("undisclosed Project relationships became dependencies: %+v %v", status, err)
	}
	if err := statusflow.New(pool).TransitionProject(ctx, statusflow.Actor{ID: admin, CompanyID: company}, project.ID, pending); err != nil {
		t.Fatal(err)
	}
	_, rows = masterRecordCSV(t, service, actor, (exports.Project{}).Kind(), exports.MasterRecordOptions{Sort: "name", Search: "Semantic"})
	if rows[0]["displayed_progress_percent"] != "37.00" || rows[0]["retained_manual_progress_percent"] != "37.00" {
		t.Fatalf("leaving Completed lost manual progress: %v", rows)
	}
	var jobStatus int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='job:new' ORDER BY id LIMIT 1`).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exports.MasterRecordOptions{Sort: "name", StatusID: jobStatus})
	if _, err := service.Accept(ctx, actor, newOperationKey(), (exports.Project{}).Kind(), raw); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("non-Project Status selection: %v", err)
	}
	client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Legacy JSON shape").SetStatusID(pending).SetCustomFields(`{"legacy":"Retained without invented definition"}`).SaveX(ctx)
	_, legacyRows := masterRecordCSV(t, service, actor, (exports.Project{}).Kind(), exports.MasterRecordOptions{Sort: "name", Search: "Legacy JSON shape"})
	var legacyFields struct {
		Values      map[string]string `json:"values"`
		Definitions []json.RawMessage `json:"definitions"`
	}
	if len(legacyRows) != 1 {
		t.Fatal("missing retained legacy JSON row")
	}
	if err := json.Unmarshal([]byte(legacyRows[0]["custom_fields_json"]), &legacyFields); err != nil {
		t.Fatal(err)
	}
	if legacyFields.Values["legacy"] != "Retained without invented definition" || len(legacyFields.Definitions) != 0 {
		t.Fatalf("legacy shape changed: %+v", legacyFields)
	}
	// The bounded value is retained in the DB; generation must fail atomically,
	// rather than silently shorten the field or publish an incomplete CSV.
	if _, err := pool.Exec(ctx, `UPDATE projects SET custom_fields=jsonb_build_array(jsonb_build_object('definition_id',$2::bigint,'value',$3::text)) WHERE id=$1`, project.ID, definition, strings.Repeat("x", 1<<20)); err != nil {
		t.Fatal(err)
	}
	id, err := service.Accept(ctx, actor, newOperationKey(), (exports.Project{}).Kind(), json.RawMessage(`{"sort":"name"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(ctx); !errors.Is(err, exports.ErrConflict) {
		t.Fatalf("oversized field generation: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_records WHERE export_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial oversized export: %d %v", count, err)
	}
}

func TestMasterRecordCursorBatchesPostgres(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cursor Customer").SaveX(ctx)
	for _, query := range []string{
		`INSERT INTO customer_contacts(company_id,customer_id,first_name) SELECT $1,$2,'Duplicate '||lpad(((n-1)/2)::text,3,'0') FROM generate_series(1,260) n`,
		`INSERT INTO locations(company_id,object_type,object_id,title) SELECT $1,'customer',$2,'Duplicate '||lpad(((n-1)/2)::text,3,'0') FROM generate_series(1,260) n`,
		`INSERT INTO projects(company_id,customer_id,name) SELECT $1,$2,'Duplicate '||lpad(((n-1)/2)::text,3,'0') FROM generate_series(1,260) n`,
	} {
		if _, err := pool.Exec(ctx, query, company, customer.ID); err != nil {
			t.Fatal(err)
		}
	}
	service := exports.New(pool, exports.Contact{}, exports.Location{}, exports.Project{})
	for _, tc := range []struct {
		definition           exports.Definition
		idColumn, nameColumn string
	}{
		{exports.Contact{}, "contact_id", "first_name"},
		{exports.Location{}, "location_id", "title"},
		{exports.Project{}, "project_id", "name"},
	} {
		t.Run(tc.definition.Kind(), func(t *testing.T) {
			_, rows := masterRecordCSV(t, service, authorization.Actor{UserID: admin}, tc.definition.Kind(), exports.MasterRecordOptions{Sort: "name_desc"})
			if len(rows) != 260 {
				t.Fatalf("cursor truncated matching rows: %d", len(rows))
			}
			seen := map[string]bool{}
			var previousID int64
			for i, row := range rows {
				want := fmt.Sprintf("Duplicate %03d", 129-i/2)
				id, err := strconv.ParseInt(row[tc.idColumn], 10, 64)
				if err != nil || seen[row[tc.idColumn]] || row[tc.nameColumn] != want || (i%2 == 1 && id >= previousID) {
					t.Fatalf("unstable/duplicate identity at %d: %v (previous %d): %v", i, row, previousID, err)
				}
				seen[row[tc.idColumn]] = true
				previousID = id
			}
		})
	}
}

func TestMasterRecordOwnArchiveAndCollectionSearchPostgres(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Customer").SaveX(ctx)
	var completed int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='project:completed' ORDER BY id LIMIT 1`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	adminActor := authorization.Actor{UserID: admin}
	for _, tc := range []struct {
		resource   string
		definition exports.Definition
	}{
		{"contact", exports.Contact{}},
		{"location", exports.Location{}},
		{"project", exports.Project{}},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			var candidate, remaining int64
			switch tc.resource {
			case "contact":
				candidate = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetFirstName("Archive candidate").SetEmail("searchneedle@example.test").SaveX(ctx).ID
				remaining = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetFirstName("Remain active").SaveX(ctx).ID
			case "location":
				candidate = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Archive candidate").SetAddress1("searchneedle address").SaveX(ctx).ID
				remaining = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Remain active").SaveX(ctx).ID
			case "project":
				candidate = client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Archive candidate").SetDescription("searchneedle description").SetStatusID(completed).SaveX(ctx).ID
				remaining = client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Remain active").SetStatusID(completed).SaveX(ctx).ID
			}
			role, err := auth.SaveRole(ctx, adminActor, 0, "Own archive "+tc.resource, []authorization.Grant{
				{Permission: tc.resource + ".view", Scope: authorization.All},
				{Permission: tc.resource + ".export", Scope: authorization.All},
			})
			if err != nil {
				t.Fatal(err)
			}
			user := client.User.Create().SetCompanyID(company).SetEmail(tc.resource + "-archive-exporter@test").SetName("Exporter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err := auth.AssignUser(ctx, adminActor, user.ID, role); err != nil {
				t.Fatal(err)
			}
			actor := authorization.Actor{UserID: user.ID}
			service := exports.New(pool, tc.definition)
			idColumn := tc.resource + "_id"
			options := exports.MasterRecordOptions{Sort: "name"}
			readyID, rows := masterRecordCSV(t, service, actor, tc.definition.Kind(), options)
			if len(rows) != 2 {
				t.Fatalf("active snapshot: %v", rows)
			}
			_, matches := masterRecordCSV(t, service, actor, tc.definition.Kind(), exports.MasterRecordOptions{Sort: "name", Search: "SEARCHNEEDLE"})
			if len(matches) != 1 || matches[0][idColumn] != strconv.FormatInt(candidate, 10) {
				t.Fatalf("collection field search: %v", matches)
			}
			_, matches = masterRecordCSV(t, service, actor, tc.definition.Kind(), exports.MasterRecordOptions{Sort: "name", Search: strconv.FormatInt(candidate, 10)})
			if len(matches) != 0 {
				t.Fatalf("ID-only search matched outside collection fields: %v", matches)
			}
			if tc.resource == "project" {
				_, matches = masterRecordCSV(t, service, actor, tc.definition.Kind(), exports.MasterRecordOptions{Sort: "name", Search: "candidate searchneedle"})
				if len(matches) != 0 {
					t.Fatalf("Project search crossed name/description boundary: %v", matches)
				}
			}
			lifecycle := func(restore bool) {
				t.Helper()
				var err error
				if tc.resource == "project" {
					err = auth.ProjectLifecycle(ctx, adminActor, candidate, restore)
				} else {
					err = auth.CustomerChildLifecycle(ctx, adminActor, customer.ID, candidate, tc.resource, restore)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			lifecycle(false)
			if download, err := service.PrepareDownload(ctx, actor, readyID); !errors.Is(err, exports.ErrUnavailable) {
				if download != nil {
					download.Close()
				}
				t.Fatalf("newly archived own row did not block whole Ready artifact: %v", err)
			}
			if status, err := service.Status(ctx, actor, readyID); err != nil || !status.Blocked || status.RowCount != nil {
				t.Fatalf("whole-result denial metadata: %+v %v", status, err)
			}
			_, rows = masterRecordCSV(t, service, actor, tc.definition.Kind(), options)
			if len(rows) != 1 || rows[0][idColumn] != strconv.FormatInt(remaining, 10) {
				t.Fatalf("own archive not excluded by default: %v", rows)
			}
			archivedOptions := exports.MasterRecordOptions{Sort: "name", Archived: true}
			raw, _ := json.Marshal(archivedOptions)
			if _, err := service.Accept(ctx, actor, newOperationKey(), tc.definition.Kind(), raw); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("own archived option missing grant accepted: %v", err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,$2,'A')`, role, tc.resource+".archived.view"); err != nil {
				t.Fatal(err)
			}
			_, rows = masterRecordCSV(t, service, actor, tc.definition.Kind(), options)
			if len(rows) != 1 {
				t.Fatalf("own archived grant alone included archive: %v", rows)
			}
			archivedID, rows := masterRecordCSV(t, service, actor, tc.definition.Kind(), archivedOptions)
			if len(rows) != 2 || rows[0][idColumn] != strconv.FormatInt(candidate, 10) || rows[0]["archived"] != "true" || rows[1]["archived"] != "false" {
				t.Fatalf("explicit own archive inclusion: %v", rows)
			}
			var retained bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM export_records WHERE export_id=$1 AND reference->>'id'=$2 AND reference->>'archived'='true')`, archivedID, strconv.FormatInt(candidate, 10)).Scan(&retained); err != nil || !retained {
				t.Fatalf("archived own authority not recorded: %t %v", retained, err)
			}
			lifecycle(true)
			if status, err := service.Status(ctx, actor, archivedID); err != nil || status.Blocked {
				t.Fatalf("restored row should remain downloadable with archived authority: %+v %v", status, err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, tc.resource+".archived.view"); err != nil {
				t.Fatal(err)
			}
			if download, err := service.PrepareDownload(ctx, actor, archivedID); !errors.Is(err, exports.ErrUnavailable) {
				if download != nil {
					download.Close()
				}
				t.Fatalf("restore discarded historical archive authority requirement: %v", err)
			}
			_, rows = masterRecordCSV(t, service, actor, tc.definition.Kind(), options)
			if len(rows) != 2 || rows[0]["archived"] != "false" {
				t.Fatalf("new active export after restore: %v", rows)
			}
			if status, err := service.Status(ctx, actor, readyID); err != nil || !status.Blocked {
				t.Fatalf("restore revived previously blocked artifact: %+v %v", status, err)
			}
		})
	}
}

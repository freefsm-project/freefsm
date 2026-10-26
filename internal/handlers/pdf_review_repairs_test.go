package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestPDFMalformedQueryNeverSelectsDefaultHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Malformed selection").SaveX(ctx)
	for _, kind := range []string{"invoice", "estimate"} {
		fieldPost(t, web, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Malformed selection"}, "line_items": {"[]"}}, 303)
		var id int64
		historyCheck(t, pool.QueryRow(ctx, "SELECT id FROM "+kind+"s").Scan(&id))
		path := fmt.Sprintf("/%ss/%d", kind, id)
		fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
		for _, query := range []string{"version=%ZZ", "version=1&version=%ZZ", "version=1;invalid", "pdf_version=%ZZ", "pdf_version=1&pdf_version=%ZZ", "version=1&confirm_stale=%ZZ"} {
			for _, entry := range []struct{ method, path, body string }{{"GET", path + "/pdf", ""}, {"GET", path + "/email", ""}, {"POST", path + "/email", "pdf_version=1"}} {
				t.Run(kind+"/"+entry.method+"/"+entry.path+"/"+query, func(t *testing.T) {
					r := httptest.NewRequest(entry.method, entry.path+"?"+query, strings.NewReader(entry.body))
					r.AddCookie(cookie)
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					w := httptest.NewRecorder()
					web.ServeHTTP(w, r)
					if w.Code != 400 {
						t.Fatalf("malformed selection returned %d, want400", w.Code)
					}
				})
			}
		}
	}
}

func TestPDFGenerationProtectedAuditOccurrenceAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Audit source").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Audit Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Audit PDF").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "PDF audit client")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body string) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s=%d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	var source struct {
		Data struct {
			Token string `json:"source_token"`
		}
	}
	historyCheck(t, json.Unmarshal(call("GET", path+"/source", ""), &source))
	for _, clientOccurrence := range []bool{true, false} {
		t.Run(fmt.Sprint(clientOccurrence), func(t *testing.T) {
			key := newOperationKey()
			extra := ""
			if clientOccurrence {
				extra = `,"occurred_at":"2026-01-02T03:04:05Z"`
			}
			intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]%s}`, key, j.ID, source.Data.Token, extra)
			var receipt struct {
				Data struct {
					Version int64 `json:"version_id"`
				}
			}
			historyCheck(t, json.Unmarshal(call("POST", path+"/generate", intent), &receipt))
			var detail []byte
			var generated time.Time
			historyCheck(t, pool.QueryRow(ctx, `SELECT a.detail,v.generated_at FROM authorization_audit a JOIN document_pdf_versions v ON v.id=(a.detail->>'pdf_version')::bigint WHERE a.action='invoice.pdf.generate' AND a.detail->>'operation_id'=$1`, key).Scan(&detail, &generated))
			var event struct {
				Operation string    `json:"operation_id"`
				Accepted  time.Time `json:"accepted_at"`
				Occurred  time.Time `json:"occurred_at"`
				Source    string    `json:"occurrence_source"`
				Version   int64     `json:"pdf_version"`
			}
			historyCheck(t, json.Unmarshal(detail, &event))
			if event.Operation != key || event.Version != receipt.Data.Version || !event.Accepted.Equal(generated) {
				t.Fatalf("unbound acceptance attribution: %s", detail)
			}
			if clientOccurrence {
				want, err := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
				historyCheck(t, err)
				if event.Source != "client" || !event.Occurred.Equal(want) || !event.Accepted.After(event.Occurred) {
					t.Fatalf("client occurrence confused with acceptance: %s", detail)
				}
			} else if event.Source != "server" || !event.Occurred.Equal(event.Accepted) {
				t.Fatalf("server occurrence missing: %s", detail)
			}
			call("POST", path+"/generate", intent)
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1`, key).Scan(&count))
			if count != 1 {
				t.Fatalf("replay wrote %d audit events", count)
			}
		})
	}
}

func TestPDFPartialHistoricalSourceIsUnknownHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical source").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Historical Job").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "History review")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	for _, kind := range []string{"invoice", "estimate"} {
		fieldPost(t, web, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"History"}, "line_items": {"[]"}}, 303)
		var id int64
		historyCheck(t, pool.QueryRow(ctx, "SELECT id FROM "+kind+"s").Scan(&id))
		path := fmt.Sprintf("/%ss/%d", kind, id)
		fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
		original := requestBody(t, web, cookie, "GET", path+"/pdf", 200)
		var current int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions WHERE document_type=$1 AND document_id=$2`, kind, id).Scan(&current))
		type sourceCase struct {
			name, expression string
			known            bool
		}
		cases := []sourceCase{
			{"current", "source_snapshot", true},
			{"legacy-unversioned", "source_snapshot-'source_format'", true},
			{"legacy-original-empty-fields", "source_snapshot-ARRAY['source_format','custom_field_values','logo']", true},
			{"unsupported-format", `source_snapshot||'{"source_format":999}'::jsonb`, false},
			{"null-format", `source_snapshot||'{"source_format":null}'::jsonb`, false},
			{"string-format", `source_snapshot||'{"source_format":"2"}'::jsonb`, false},
			{"missing-customer", "source_snapshot-'customer'", false},
			{"missing-job", "source_snapshot-'job'", false},
			{"missing-asset", "source_snapshot-'asset'", false},
			{"missing-status", "source_snapshot-'status'", false},
			{"partial-customer", "source_snapshot #- '{customer,phone}'", false},
			{"missing-fields-v2", "source_snapshot-'custom_field_values'", false},
			{"missing-logo-v2", "source_snapshot-'logo'", false},
			{"unknown", "'{}'::jsonb", false},
		}
		if kind == "invoice" {
			cases = append(cases,
				sourceCase{"missing-allocations", "source_snapshot-'allocations'", false},
				sourceCase{"missing-applications", "source_snapshot-'credit_applications'", false},
				sourceCase{"legacy-missing-allocations", "source_snapshot-ARRAY['source_format','allocations']", false},
				sourceCase{"legacy-missing-applications", "source_snapshot-ARRAY['source_format','credit_applications']", false},
				sourceCase{"missing-invoice-date", "source_snapshot #- '{document,invoice_date}'", false},
				sourceCase{"invalid-settlement-shape", `source_snapshot||'{"allocations":{}}'::jsonb`, false},
				sourceCase{"missing-reversal-evidence", `source_snapshot||'{"allocations":[{"invoice_id":1,"payment_id":"retained","amount_cents":1}]}'::jsonb`, false},
			)
		}
		for _, test := range cases {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				var version int64
				historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,source_snapshot,filename,content,generated_by) SELECT company_id,document_type,document_id,source_token,`+test.expression+`,filename,content,generated_by FROM document_pdf_versions WHERE id=$1 RETURNING id`, current).Scan(&version))
				before := historyDurableRows(t, pool, []string{"document_pdf_versions"})
				r := httptest.NewRequest("GET", path+"/pdf/versions", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("metadata=%d", w.Code)
				}
				var listing struct {
					Data struct {
						Default  int64 `json:"default_version"`
						Versions []struct {
							ID    int64 `json:"id"`
							Known bool  `json:"source_known"`
							Stale bool  `json:"stale"`
						} `json:"versions"`
					}
				}
				historyCheck(t, json.Unmarshal(w.Body.Bytes(), &listing))
				found := false
				for _, v := range listing.Data.Versions {
					if v.ID == version {
						found = true
						if v.Known != test.known || v.Stale == test.known {
							t.Errorf("source classification known=%v stale=%v want known=%v", v.Known, v.Stale, test.known)
						}
					}
				}
				if !found {
					t.Fatal("missing retained version")
				}
				if !test.known && listing.Data.Default == version {
					t.Error("partial history became default")
				}
				want := 409
				if test.known {
					want = 200
				}
				expectStatus(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d", path, version), want)
				if !test.known {
					assertContains(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "Historical source is unavailable")
				}
				if got := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version), 200); got != original {
					t.Fatal("retained bytes changed")
				}
				historyEqual(t, "classification must not rewrite captured facts", historyDurableRows(t, pool, []string{"document_pdf_versions"}), before)
			})
		}
	}
}

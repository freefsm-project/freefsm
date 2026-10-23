package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Hold the first command at its actual effect statement, after request-time
// checks and while its committing transaction owns the shared authority fence.
// A fixture-only trigger exposes this seam; pg_blocking_pids proves the second
// request is blocked by that transaction, not merely by an earlier read check.
func conversionOrderedEffects(t *testing.T, f *creditMatrix, first, second creditCommand, event string) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `CREATE FUNCTION conversion_effect_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(540054); RETURN NEW; END $$; CREATE TRIGGER conversion_effect_barrier BEFORE `+event+` FOR EACH ROW EXECUTE FUNCTION conversion_effect_barrier()`)
	historyCheck(t, err)
	tx, err := f.pool.Begin(f.ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	var holder, firstPID, secondPID int
	historyCheck(t, tx.QueryRow(f.ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(540054)`).Scan(&holder))
	a, b := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { a <- f.call(first) }()
	commercialWaiters(t, f.ctx, f.pool, holder, 1)
	app := f.pool.Config().ConnConfig.RuntimeParams["application_name"]
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, app).Scan(&firstPID))
	go func() { b <- f.call(second) }()
	commercialWaiters(t, f.ctx, f.pool, firstPID, 1)
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, firstPID, app).Scan(&secondPID))
	t.Logf("observed actual-effect barrier holder=%d first_writer=%d second=%d event=%s first=%s second_path=%s", holder, firstPID, secondPID, event, first.path, second.path)
	historyCheck(t, tx.Commit(f.ctx))
	return <-a, <-b
}

func TestConversionQueuedGrantRevocationHTTP(t *testing.T) {
	for _, conversionFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(conversionFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Conversion before revocation", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.convert", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.create", Scope: authorization.All}})
			historyCheck(t, err)
			empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No grants", nil)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("conversion-revoke@test").SetName("Converter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Revocation").SaveX(ctx)
			fieldPost(t, web, adminCookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Revocation source"}, "line_items": {"[]"}}, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id))
			path := fmt.Sprintf("/estimates/%d/convert", id)
			convert := creditCommand{path, conversionReview(t, web, cookie, path)}
			revoke := creditCommand{"/settings/authorization/assign", url.Values{"user_id": {fmt.Sprint(u.ID)}, "role_id": {fmt.Sprint(empty)}}}
			mixed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == revoke.path {
					r.Header.Del("Cookie")
					r.AddCookie(adminCookie)
				}
				web.ServeHTTP(w, r)
			})
			f := &creditMatrix{ctx: ctx, pool: pool, web: mixed, cookie: cookie}
			a, b, event := convert, revoke, "UPDATE OF conversion_hidden_at ON estimates"
			if !conversionFirst {
				a, b, event = revoke, convert, "UPDATE OF authorization_role_id ON users"
			}
			ra, rb := conversionOrderedEffects(t, f, a, b, event)
			conversionResult, revocationResult := ra, rb
			if !conversionFirst {
				conversionResult, revocationResult = rb, ra
			}
			want := 303
			if !conversionFirst {
				want = 403
			}
			if conversionResult.Code != want || revocationResult.Code != 303 {
				t.Fatalf("conversion=%d %s revocation=%d %s", conversionResult.Code, conversionResult.Body.String(), revocationResult.Code, revocationResult.Body.String())
			}
			var cycles, audits int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimate_invoice_conversion_cycles),(SELECT count(*) FROM authorization_audit WHERE action='estimate.convert' AND actor_id=$1)`, u.ID).Scan(&cycles, &audits))
			if cycles != boolInt(conversionFirst) || audits != cycles {
				t.Fatalf("revocation partial effect cycles=%d audit=%d", cycles, audits)
			}
		})
	}
}

func TestConversionObservedWriterOrderHTTP(t *testing.T) {
	for _, direction := range []string{"convert", "revert"} {
		for _, other := range []string{"same-command", "paired-value", "pairing", "required-definition", "comment", "tag", "file"} {
			for _, conversionFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/conversion_first_%v", direction, other, conversionFirst), func(t *testing.T) {
					pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
					ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
					defer cancel()
					cookie := sessionCookie(t, ctx, sessions, admin)
					f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
					c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race Customer").SaveX(ctx)
					for _, kind := range []string{"estimate", "invoice"} {
						fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"object_type": {kind}, "name": {kind + " race"}, "field_type": {"text"}, "conversion_key": {"race"}}, 303)
					}
					fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Race source"}, "line_items": {"[]"}, "custom_field_1": {"Original"}}, 303)
					var estimate int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&estimate))
					id, kind, action := estimate, "estimate", "convert"
					if direction == "revert" {
						fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/convert", estimate), url.Values{"idempotency_key": {newOperationKey()}}, 303)
						historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, estimate).Scan(&id))
						kind, action = "invoice", "revert-to-estimate"
					}
					path := fmt.Sprintf("/%ss/%d", kind, id)
					conversionUpload(t, web, cookie, kind, id, "race.txt", "stable race bytes")
					fieldPost(t, web, cookie, "/tags", url.Values{"name": {"Race tag"}}, 303)
					var tag, file, revision int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM tags`).Scan(&tag))
					historyCheck(t, pool.QueryRow(ctx, `SELECT id,revision FROM files`).Scan(&file, &revision))
					primary := creditCommand{path + "/" + action, conversionReview(t, web, cookie, path+"/"+action)}
					var contender creditCommand
					switch other {
					case "same-command":
						contender = creditCommand{primary.path, conversionReview(t, web, cookie, primary.path)}
					case "paired-value":
						field := "custom_field_1"
						if direction == "revert" {
							field = "custom_field_2"
						}
						contender = creditCommand{path + "/custom-fields", url.Values{field: {"Racing value"}}}
						contender.values = currentJobFormValues(t, web, cookie, contender.path, contender.values)
					case "pairing":
						contender = creditCommand{"/settings/custom-fields/2", url.Values{"name": {"invoice race"}, "field_type": {"text"}, "conversion_key": {"future"}}}
					case "required-definition":
						target := "invoice"
						if direction == "revert" {
							target = "estimate"
						}
						contender = creditCommand{"/settings/custom-fields", url.Values{"name": {"New required"}, "object_type": {target}, "field_type": {"checkbox"}, "required": {"true"}}}
					case "comment":
						contender = creditCommand{path + "/comments", url.Values{"content": {"Racing Comment"}, "operation_id": {newOperationKey()}}}
					case "tag":
						contender = creditCommand{fmt.Sprintf("%s/tags/%d/attach", path, tag), url.Values{}}
					case "file":
						contender = creditCommand{fmt.Sprintf("/files/%d/rename", file), url.Values{"original_name": {"renamed.txt"}, "revision": {fmt.Sprint(revision)}}}
					}
					a, b := primary, contender
					if !conversionFirst {
						a, b = b, a
					}
					event := "UPDATE OF conversion_hidden_at ON " + kind + "s"
					if !conversionFirst {
						switch other {
						case "paired-value":
							event = "UPDATE OF custom_fields ON " + kind + "s"
						case "pairing":
							event = "UPDATE ON custom_field_definitions"
						case "required-definition":
							event = "INSERT ON custom_field_definitions"
						case "comment":
							event = "INSERT ON comments"
						case "tag":
							event = "INSERT ON tag_links"
						case "file":
							event = "UPDATE OF original_name ON files"
						}
					}
					ra, rb := conversionOrderedEffects(t, f, a, b, event)
					p, o := ra, rb
					if !conversionFirst {
						p, o = rb, ra
					}
					wantPrimary := 303
					if !conversionFirst && (other == "paired-value" || other == "same-command") {
						wantPrimary = 409
					}
					if !conversionFirst && other == "required-definition" && direction == "convert" {
						wantPrimary = 422
					}
					if p.Code != wantPrimary {
						t.Fatalf("conversion=%d want%d: %s; other=%d %s", p.Code, wantPrimary, p.Body.String(), o.Code, o.Body.String())
					}
					if other == "same-command" {
						want := 303
						if conversionFirst {
							want = 409
						}
						if o.Code != want {
							t.Fatalf("other conversion=%d %s", o.Code, o.Body.String())
						}
					} else if !conversionFirst || other == "pairing" || other == "required-definition" {
						want := 200
						if other == "paired-value" || other == "pairing" || other == "required-definition" {
							want = 303
						}
						if other == "comment" && o.Code == 202 {
							if o.Header().Get("X-FreeFSM-Comment-Operation") != contender.values.Get("operation_id") {
								t.Fatal("accepted Comment lost its operation identity")
							}
							want = 202
						}
						if o.Code != want {
							t.Fatalf("other=%d want%d %s", o.Code, want, o.Body.String())
						}
					} else if o.Code < 400 {
						t.Fatalf("stale collaboration/value request silently followed conversion: %d %s", o.Code, o.Body.String())
					}
					var fileCount, comments, tags int
					historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM files),(SELECT count(*) FROM comments),(SELECT count(*) FROM tag_links)`).Scan(&fileCount, &comments, &tags))
					if fileCount != 1 || comments != boolInt(other == "comment" && !conversionFirst) || tags != boolInt(other == "tag" && !conversionFirst) {
						t.Fatalf("relation inventory files=%d comments=%d tags=%d", fileCount, comments, tags)
					}
					var ownerKind string
					var owner int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT object_type,object_id FROM files`).Scan(&ownerKind, &owner))
					changed := p.Code == 303 || other == "same-command"
					if changed {
						if direction == "convert" {
							var invoice int64
							historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, estimate).Scan(&invoice))
							if ownerKind != "invoice" || owner != invoice {
								t.Fatal("file owner differs from active cycle")
							}
						} else if ownerKind != "estimate" || owner != estimate {
							t.Fatal("file did not return to original Estimate")
						}
					} else if ownerKind != kind || owner != id {
						t.Fatal("failed conversion moved relations")
					}
					var mismatched int
					historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM comments WHERE object_type<>$1 OR object_id<>$2)+(SELECT count(*) FROM tag_links WHERE object_type<>$1 OR object_id<>$2)`, ownerKind, owner).Scan(&mismatched))
					if mismatched != 0 {
						t.Fatal("relations have different current owners")
					}
					if other == "pairing" {
						var pairs string
						historyCheck(t, pool.QueryRow(ctx, `SELECT source_snapshot->'field_pairs' FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 ORDER BY converted_at DESC LIMIT 1`, estimate).Scan(&pairs))
						paired := direction == "revert" || conversionFirst
						if strings.Contains(pairs, `"1"`) != paired {
							t.Fatalf("captured pairing follows wrong order: %s", pairs)
						}
					}
				})
			}
		}
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestConversionOldReviewCannotRaceReversionIntoNewCycleHTTP(t *testing.T) {
	for _, revertFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(revertFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cycle ABA").SaveX(ctx)
			fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Cycle ABA"}, "line_items": {"[]"}}, 303)
			var id, invoice int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id))
			path := fmt.Sprintf("/estimates/%d/convert", id)
			old := creditCommand{path, conversionReview(t, web, cookie, path)}
			fieldPost(t, web, cookie, path, url.Values{"idempotency_key": {newOperationKey()}}, 303)
			historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, id).Scan(&invoice))
			revertPath := fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice)
			revert := creditCommand{revertPath, conversionReview(t, web, cookie, revertPath)}
			var r, o *httptest.ResponseRecorder
			if revertFirst {
				r, o = conversionOrderedEffects(t, f, revert, old, "UPDATE OF conversion_hidden_at ON invoices")
			} else {
				o, r = f.ordered(t, old, revert)
			}
			if r.Code != 303 || o.Code != 409 {
				t.Fatalf("revert=%d %s old convert=%d %s", r.Code, r.Body.String(), o.Code, o.Body.String())
			}
			code := "stale_conversion_cycle"
			if revertFirst {
				code = "stale_commercial_revision"
			}
			assertContains(t, o.Body.String(), code)
			var cycles, active int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE reverted_at IS NULL) FROM estimate_invoice_conversion_cycles`).Scan(&cycles, &active))
			if cycles != 1 || active != 0 {
				t.Fatal("old review created a later cycle", cycles, active)
			}
		})
	}
}

func TestConversionRevertSettlementObservedOrderHTTP(t *testing.T) {
	for _, revertFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(revertFirst), func(t *testing.T) {
			f := newCreditMatrix(t)
			f.post(t, creditCommand{"/estimates", url.Values{"customer_id": {fmt.Sprint(f.customer)}, "title": {"Settlement conversion"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}}}, 303)
			var estimate, invoice int64
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM estimates`).Scan(&estimate))
			f.post(t, creditCommand{fmt.Sprintf("/estimates/%d/convert", estimate), url.Values{"idempotency_key": {newOperationKey()}}}, 303)
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, estimate).Scan(&invoice))
			path := fmt.Sprintf("/invoices/%d", invoice)
			fieldPost(t, f.web, f.cookie, path+"/finalize", url.Values{}, 303)
			revert := creditCommand{path + "/revert-to-estimate", conversionReview(t, f.web, f.cookie, path+"/revert-to-estimate")}
			payment := creditCommand{path + "/payments-with-credit", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"25.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}
			a, b := revert, payment
			if !revertFirst {
				a, b = b, a
			}
			event := "UPDATE OF conversion_hidden_at ON invoices"
			if !revertFirst {
				event = "INSERT ON invoice_payments"
			}
			ra, rb := conversionOrderedEffects(t, f, a, b, event)
			r, p := ra, rb
			if !revertFirst {
				r, p = rb, ra
			}
			if revertFirst && (r.Code != 303 || p.Code < 400) || !revertFirst && (p.Code != 303 || r.Code != 409) {
				t.Fatalf("revert=%d %s payment=%d %s", r.Code, r.Body.String(), p.Code, p.Body.String())
			}
			var hidden bool
			var payments int
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT conversion_hidden_at IS NOT NULL,(SELECT count(*) FROM invoice_payments WHERE invoice_id=$1) FROM invoices WHERE id=$1`, invoice).Scan(&hidden, &payments))
			if hidden != revertFirst || payments != boolInt(!revertFirst) {
				t.Fatalf("partial settlement/reversion hidden=%v payments=%d", hidden, payments)
			}
		})
	}
}

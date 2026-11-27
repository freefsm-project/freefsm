package authorization_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

func TestListStateDocumentNormalizerUnicodeContract(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		if _, err := authorization.NormalizeDocumentCollectionFilter(kind, authorization.DocumentListFilter{Search: strings.Repeat("界", 500)}); err != nil {
			t.Fatalf("%s rejected 500 runes: %v", kind, err)
		}
		for _, filter := range []authorization.DocumentListFilter{
			{Search: strings.Repeat("界", 501)}, {Search: "\xff"}, {Sort: "unsupported"}, {StatusID: -1},
		} {
			if _, err := authorization.NormalizeDocumentCollectionFilter(kind, filter); !errors.Is(err, authorization.ErrConflict) {
				t.Fatalf("%s malformed query is not a domain conflict: %v", kind, err)
			}
		}
	}
}

func TestListStateAuthorizedSaveFencesViewAndSensitiveFacet(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		grants, retained []authorization.Grant
		query            authorization.DiscoveryQuery
	}{
		{"view", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}}, nil,
			authorization.DiscoveryQuery{Types: []string{"item"}}},
		{"archive", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.archived.view", Scope: authorization.All}},
			[]authorization.Grant{{Permission: "item.view", Scope: authorization.All}},
			authorization.DiscoveryQuery{Types: []string{"item"}, State: "archived"}},
		{"billing", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.billing.view", Scope: authorization.All}},
			[]authorization.Grant{{Permission: "lease.view", Scope: authorization.All}},
			authorization.DiscoveryQuery{Types: []string{"lease"}, Lease: authorization.LeaseWorkspaceQuery{Billing: "failed"}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, svc := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var admin, user int64
			if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role)
				SELECT id,'state-fence-admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id`).Scan(&admin); err != nil {
				t.Fatal(err)
			}
			adminActor := authorization.Actor{UserID: admin}
			role, err := svc.SaveRole(ctx, adminActor, 0, "State fence", scenario.grants)
			if err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id)
				SELECT id,'state-fence-user@test','Reader','hash','tech',$1 FROM companies LIMIT 1 RETURNING id`, role).Scan(&user); err != nil {
				t.Fatal(err)
			}
			actor := authorization.Actor{UserID: user}
			if _, err := pool.Exec(ctx, `CREATE TABLE list_state_commit_order(id bigserial PRIMARY KEY,kind text NOT NULL);
				CREATE FUNCTION list_state_capture_commit() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN INSERT INTO list_state_commit_order(kind) VALUES(TG_ARGV[0]); RETURN NEW; END $$;
				CREATE TRIGGER capture_state BEFORE INSERT OR UPDATE ON list_states FOR EACH ROW EXECUTE FUNCTION list_state_capture_commit('state');
				CREATE TRIGGER capture_revoke BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION list_state_capture_commit('revoke');`); err != nil {
				t.Fatal(err)
			}
			admitted := make(chan int, 1)
			release := make(chan struct{})
			saved := make(chan error, 1)
			values := url.Values{"q": {"fenced"}, "type": scenario.query.Types}
			collectionKey := scenario.query.Types[0] + "s"
			collectionValues := url.Values{"q": {"fenced"}}
			if scenario.query.State == "archived" {
				collectionValues.Set("archived", "1")
			}
			if scenario.query.Lease.Billing != "" {
				collectionValues.Set("billing", scenario.query.Lease.Billing)
			}
			go func() {
				_, err := svc.SaveAuthorizedListState(ctx, actor, "search", values, 0, func(tx pgx.Tx, p authorization.Principal) error {
					if err := authorization.ValidateDiscoveryQuery(ctx, tx, p, scenario.query); err != nil {
						return err
					}
					if err := authorization.ValidateCollectionListQuery(ctx, tx, p, collectionKey, collectionValues); err != nil {
						return err
					}
					var pid int
					if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						return err
					}
					admitted <- pid
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
				saved <- err
			}()
			var pid int
			select {
			case pid = <-admitted:
			case err := <-saved:
				t.Fatalf("save before admission: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			revoked := make(chan error, 1)
			go func() {
				_, err := svc.SaveRole(ctx, adminActor, role, "State fence", scenario.retained, 1)
				revoked <- err
			}()
			// Observe the real authority-lock wait; no sleep or scheduler-order
			// assumption substitutes for the concurrent revocation barrier.
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-revoked:
					t.Fatalf("revocation escaped save fence: %v", err)
				default:
				}
			}
			close(release)
			if err := <-saved; err != nil {
				t.Fatal(err)
			}
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			var order string
			if err := pool.QueryRow(ctx, `SELECT string_agg(kind,',' ORDER BY id) FROM list_state_commit_order`).Scan(&order); err != nil || order != "state,revoke" {
				t.Fatalf("unsafe commit order: %s %v", order, err)
			}
			// The same saved presentation revalidated after the revocation must
			// fail, including when only independent archive/Billing was removed.
			got, err := svc.SaveAuthorizedListState(ctx, actor, "search", values, 1, func(tx pgx.Tx, p authorization.Principal) error {
				return authorization.ValidateDiscoveryQuery(ctx, tx, p, scenario.query)
			})
			if !errors.Is(err, authorization.ErrForbidden) || got.Values != nil || got.Revision != 0 {
				t.Fatalf("revoked query persisted: %+v %v", got, err)
			}
			got, err = svc.SaveAuthorizedListState(ctx, actor, collectionKey, collectionValues, 0, func(tx pgx.Tx, p authorization.Principal) error {
				return authorization.ValidateCollectionListQuery(ctx, tx, p, collectionKey, collectionValues)
			})
			if !errors.Is(err, authorization.ErrForbidden) || got.Values != nil || got.Revision != 0 {
				t.Fatalf("revoked dedicated query persisted: %+v %v", got, err)
			}
			if err := pool.QueryRow(ctx, `SELECT string_agg(kind,',' ORDER BY id) FROM list_state_commit_order`).Scan(&order); err != nil || order != "state,revoke" {
				t.Fatalf("denial left effects: %s %v", order, err)
			}
			if _, err := svc.ResetListState(ctx, actor, "search", 1); err != nil {
				t.Fatalf("intrinsic Reset denied after grant loss: %v", err)
			}
		})
	}
}

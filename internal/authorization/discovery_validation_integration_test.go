package authorization_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

func TestDiscoveryValidationUsesSuppliedTransaction(t *testing.T) {
	f := newCommercialReportFixture(t)
	a := discoveryActor(t, f,
		authorization.Grant{Permission: "lease.view", Scope: authorization.All},
		authorization.Grant{Permission: "customer.view", Scope: authorization.All},
		authorization.Grant{Permission: "item.view", Scope: authorization.All})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rollback := errors.New("rollback transaction-local visibility fixture")
	var customer int64
	err := f.svc.WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		// Only this transaction can see the freshly inserted relation target.
		// A pool read would deny it; nested WithinActor would deadlock.
		if err := tx.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Uncommitted discovery target') RETURNING id`, f.company).Scan(&customer); err != nil {
			return err
		}
		q := authorization.DiscoveryQuery{Types: []string{"lease"}, Search: "no records need to match",
			Lease: authorization.LeaseWorkspaceQuery{Customer: customer}}
		if err := authorization.ValidateDiscoveryQuery(ctx, tx, p, q); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, customer); err != nil {
			return err
		}
		if err := authorization.ValidateDiscoveryQuery(ctx, tx, p, q); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("transaction-local archive was not reauthorized: %v", err)
		}
		// Validation must not execute the discovery engine or normalize the
		// caller's selected-type slice in place.
		if _, err := tx.Exec(ctx, `ALTER TABLE items RENAME TO discovery_validation_unavailable_items`); err != nil {
			return err
		}
		types := []string{"item", "customer"}
		if err := authorization.ValidateDiscoveryQuery(ctx, tx, p, authorization.DiscoveryQuery{Types: types}); err != nil {
			return err
		}
		if !reflect.DeepEqual(types, []string{"item", "customer"}) {
			t.Fatal("validator mutated caller input")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction-aware validation: %v", err)
	}
	var exists bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1)`, customer).Scan(&exists); err != nil || exists {
		t.Fatalf("validation fixture escaped rollback: exists=%v err=%v", exists, err)
	}
}

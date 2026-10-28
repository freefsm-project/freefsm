package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestCustomerCascadeReceiptSealsWithEffectsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic archive receipt").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Receipt child"}, "is_primary": {"true"}}, 303)
	var preview authorization.CustomerArchivePreview
	masterJSON(t, web, cookie, base+"/archive", &preview)
	before := historyDurableRows(t, pool, []string{"customers", "customer_contacts", "authorization_audit", "activity_logs"})
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_cascade_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.domain_committed_at IS NOT NULL AND OLD.domain_committed_at IS NULL THEN RAISE EXCEPTION 'injected seal failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_cascade_receipt BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION fail_cascade_receipt()`)
	historyCheck(t, err)
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {preview.Snapshot}, "save_action_id": {uuid.NewString()}}, 409)
	historyEqual(t, "receipt seal failure rolls back entire cascade", historyDurableRows(t, pool, []string{"customers", "customer_contacts", "authorization_audit", "activity_logs"}), before)
	_, err = pool.Exec(ctx, `DROP TRIGGER fail_cascade_receipt ON online_save_actions`)
	historyCheck(t, err)
	id := uuid.NewString()
	values := url.Values{"archive_snapshot": {preview.Snapshot}, "save_action_id": {id}}
	masterPost(t, web, cookie, base+"/delete", values, 303)
	var sealed bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT domain_committed_at IS NOT NULL FROM online_save_actions WHERE operation_id=$1`, id).Scan(&sealed))
	if !sealed {
		t.Fatal("cascade receipt is not committed with effects")
	}
	after := historyDurableRows(t, pool, []string{"customers", "customer_contacts", "authorization_audit", "activity_logs"})
	// Replaying the same identity reconciles its receipt instead of new intent.
	masterPost(t, web, cookie, base+"/delete", values, 409)
	historyEqual(t, "receipt replay cannot repeat effects", historyDurableRows(t, pool, []string{"customers", "customer_contacts", "authorization_audit", "activity_logs"}), after)
}

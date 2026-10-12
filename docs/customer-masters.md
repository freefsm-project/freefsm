# Customer, Contact and Location masters (#31)

## Office workflows

Customers require an explicit display name. Individual/Organization kind is
editable; the stored `individual`/`company` values remain compatible with existing
identities. Legacy CRM columns remain recorded, but are neither displayed as a
lifecycle nor accepted by the Customer editor. Duplicate identity/contact/address
values are valid.

Customer pages link to independently authorized Contact and Location lists,
details, forms and archive/restore actions. Ownership is the immutable Customer in
the record's address. To move a person or place, create a new record. Contact name
may use either or both name fields. Location title is required. Service Notes are
separate from internal notes; Location Access Notes are separately recorded.

Each Customer may have zero or one active Primary Contact and Primary Location.
Selecting a new primary clears the previous designation in the same transaction.
These are office defaults, never implicit Job links or context grants. Archiving
or restoring a child never selects a replacement primary.

Lists use 25/50/100 (default 25), case-insensitive name-plus-ID order, and URL query
state. Customer search includes authorized active Contact name/email/phone and
Location title/address. A relationship match names the matching record and ID.
Archived Customers/children require explicit inclusion and their separate grants.

The dashboard's Related Work contains only directly owned Projects, Jobs,
Estimates and Invoices, once each, with permissions and lifecycle filtering before
counts and recent ranking. Each card has a count and a paginated View all route.
Contacts/Locations and activity remain separate. Receivables use the shared exact
pricing calculation, active settlement effects and positive due on authorized
active non-Draft/non-Void Invoices. The six buckets are Current, 1–30, 31–60,
61–90, 91+ and No Due Date. Credit is independently granted and never netted;
settlement history requires its own View grant.

## Archive and concurrency

Contact archive rejects unfinished Job dependencies. Location archive also
rejects unfinished Projects. Finished links remain recorded. Customer archive
requires an exact preview, every affected child type's independent Archive grant,
and a fresh matching snapshot. Active work, outstanding/Draft Invoices, available
Credit and active Assets block Customer archive. The reviewed cascade includes
active Contacts and Locations. Customer restore restores only the Customer.

The current Asset schema has no ownership classes: this slice conservatively
blocks active Assets rather than guessing which can be cascaded. Customer-owned
Asset cascade/Company-owned placement rules are integrated by the Asset slice;
Lease premises blockers belong to the Lease slice. Job/Project/commercial action
authorization and full detail-route migration remain in their owning tickets.
This slice's related-work projection checks their explicit View scopes directly.

Master commands use the foundation authorization transaction lock, re-read
identity/session/grants, lock the Customer, and commit effects with immutable
audit. Database ownership guards and partial unique indexes enforce immutability
and primary uniqueness. Job/Project selection writers share the serialization
boundary and reject stale archived/foreign Contact/Location selections. Restoring
finished work can retain historical archived links; reopening requires repair.

## Upgrade preflight

Before upgrading, run the read-only inventory against the installation database:

```sh
FREEFSM_MASTER_PREFLIGHT_DATABASE_URL='postgresql://…' \
  go run ./cmd/freefsm-master-preflight
```

The JSON report preserves observed IDs, kind, owners and primary flags. It reports
ambiguous kind, duplicate primaries, mismatched Job links, and the absence of
historical ownership evidence. Review those facts explicitly. Do not merge valid
duplicate people/places, reconstruct supposed earlier owners, or choose a primary
by row order. Resolve duplicate primary designations explicitly before retrying:
migration 058 rejects that case transactionally before altering records. All
changes are additive; no migration grants permissions to a nonprotected Role.

## Closed field context

`GET /jobs/{id}/master-context` is available in the browser and field API. It
requires a current assignment and active Job and projects only the explicitly
linked Contact/Location for which Context View is granted. It never substitutes
primaries or includes siblings, internal notes, billing fields or master editing.
The existing browser Customer context page also includes this closed projection.
Public API master maintenance remains excluded.

## Evidence

`internal/handlers/customer_masters_integration_test.go` exercises production
HTTP over the migrated database: validation, independent grants, archive/restore,
primary contention, audit failure rollback, grant revocation while waiting,
selection/archive races, relationship search, pagination, filtered related-work
counts, aging and independent Credit, closed API/browser context and query failure.
The persisted Chromium script is `testdata/browser/customer-masters.cjs` in that
package. It uses actual forms, CSRF, database persistence, mobile layout, search,
duplicate Contacts, primary Locations, reviewed cascade and selective restore.

`internal/database/customer_master_preflight_integration_test.go` checks the
read-only preflight, rejected ambiguous-primary upgrade, explicit repair,
identity/link/duplicate preservation and immutable ownership after migration.

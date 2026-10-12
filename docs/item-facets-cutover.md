# Item facets and catalog selection (#43)

## Authority and surfaces

There is one Item identity. `item.view/A` exposes name, editable Service/Product
type, description and operational flags. `pricebook.view/A` additionally exposes
SKU, price and tax. `cost.view/A` exposes Cost independently. Base Create/Edit,
Pricebook Create/Edit and Cost Edit are checked separately for submitted fields;
omitted facets retain their stored values. Cost Export remains an independent
catalog grant with Cost View and base View prerequisites. Master export delivery
is the separate #97 slice. Customer Context never supplies Item authority.

The browser Item form and navigation use effective grants, not legacy role names.
Inline creation in commercial editors requires Item Create and Pricebook Create.
It never requests or copies Cost; authorized Cost maintenance is on the Item form.
Legacy Ent readers filter Item access and archived visibility before pagination,
redact unauthorized facets, and filter Item activity before activity pagination.
Product-request Ent Item mutations fail closed; the transaction-bound Item
commands are the maintenance boundary.

The public field API adds only:

- `GET /api/v1/items?search=...`: active Item references, stable name/ID order,
  bounded to 200 results. Search matches name/description and, with Pricebook
  View, SKU. Narrow the search to select from larger catalogs.
- `GET /api/v1/items/{id}`: the same reference projection for one active Item.
- `GET /api/v1/items/{id}/selection`: requires base and Pricebook View and returns
  a reference suitable for an editable captured line.

Responses use the existing `{ "data": ... }` envelope. Price is an exact decimal
string. Without Pricebook View its fields are absent, not fabricated zeros.
These field projections always omit Cost, even for an Administrator; the field
selection surface has no Cost purpose. Invisible Items return 404; an unavailable
selection action on a visible Item returns 403. There are no Item maintenance,
archive or export endpoints on the field API.

`internal/api/v1/testdata/item-reference-harness.cjs` is the bounded executable
Item reference harness: allowlisted cache, editable staged capture, explicit
review without repricing, and cache purge on failed authorization/logout. Its
conformance runner uses the real bearer-authenticated API, including session
revocation. The integration test submits its capture through the existing
commercial HTTP writer after price/type drift and verifies rejection after
archive. This is Item reference/selection coverage, not a claim that the later
field-document operation/receipt protocol (#88) is implemented here.

## Commercial capture boundary

Job, Estimate and Invoice writers validate newly selected Item identities inside
the commercial transaction. They share the foundation authorization mutex, lock
the selected Item rows, and recheck the session before commit. New selections
require current base/Pricebook View and an active, unarchived Item. Selection
evidence uses the shared immutable product audit writer in the same transaction;
failure rolls back the whole commercial edit.

Existing captures are never compared against today's catalog price or type.
Provenance occurrences are counted: appending another line with the same Item is
a new selection; editing a retained line's captured values is not. Free-text
lines have no required Item provenance. Server-owned copy commands carry a
trusted source record, allowing existing Job captures to transfer to a document
or recurring Job without turning an archived historical reference into a new
catalog selection. The current source is read under the same transaction lock.
Estimate/Invoice conversion also copies stored snapshots, not catalog values.

Other commercial action grants and the broader Job pricing UI belong to their
own slices; this change integrates the Item input invariant into existing writers.

## Reviewed upgrade

Migration `058_item_facets` is forward-only and transactional. Before any schema
change commits it rejects blank names, invalid types, negative/nonfinite prices
or Costs, invalid tax rates, and whitespace-ambiguous SKUs. The error identifies
the affected Item IDs, without logging their sensitive values. It neither repairs
values silently nor creates historical captures.

Before upgrading, back up the installation and review the Item records using the
existing authorized product access and operator maintenance procedure. If the
migration rejects records, review the listed IDs with the business owner, record
the explicit resolution, correct only the known invalid values, and rerun the
upgrade. Do not replace unknown legacy Cost or pricing with invented zeros. A
failed migration rolls back its own changes and must not be bypassed to serve
traffic. Rollback is the prior binary plus its matching backup.

Valid Item rows, IDs, references, archived flags, recorded Cost, and all Job and
document captured JSON remain unchanged. Duplicate names become valid. Existing
nonblank SKU uniqueness stays global, including archived Items. A claim ledger
reserves each known SKU permanently for its original Item, also after SKU edits.
The migration records currently known claims only; it cannot invent the ownership
of SKUs changed before this release. No Item permission is newly introduced here:
the #19 catalog already contains the facet grants. No Role's grants are changed
by this migration, and existing nonprotected Roles receive no automatic grants.

## Regression evidence

- `internal/handlers/item_permissions_integration_test.go`: production HTTP
  grants/facet denial, repeated names, invalid input, Cost-only editing, inline
  creation, non-disclosing search/history, API allowlist, persisted Chromium,
  offline reference harness, all three commercial writers, captured Job copies,
  concurrent SKU claims, waiting archive/revocation, session expiry at commit,
  and maintenance/selection audit-failure rollback.
- `internal/database/item_upgrade_integration_test.go`: rejected invalid legacy
  upgrade, explicit repair and repeat migration; byte-equivalent recorded Item,
  Job and grant facts; archived/SKU-change reservation and duplicate names.
- Existing foundation catalog tests retain the Cost/base/facet prerequisite and
  fresh-versus-upgrade grant guarantees. Existing conversion and line-item tests
  retain captured pricing and free-text behavior.

No mutable activity is backfilled as immutable historical evidence. The shared
audit adapter accepts the existing pgx transactions and the handwritten Ent
transaction adapter; callers must supply the effect transaction, never a pool.

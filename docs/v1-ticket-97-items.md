# Ticket 97: Item CSV facets

Normative sources: live issues [97](https://github.com/freefsm-project/freefsm/issues/97)
and [18](https://github.com/freefsm-project/freefsm/issues/18), especially PER-05,
PER-10, PER-12, MAS-07 and DISC-05.

## Integration contract

Register `exports.ItemBase{}`, `exports.Pricebook{}`, and `exports.Cost{}` in the
shared durable export registry. Their kinds are `item-base-v1`, `pricebook-v1`,
and `item-cost-v1`. Each uses scope A exclusively.

| Kind | Required grants (all A) |
| --- | --- |
| Item base | `item.export`, `item.view` |
| Pricebook | `pricebook.export`, `pricebook.view`, `item.view` |
| Item Cost | `cost.export`, `cost.view`, `item.view` |

Cost does not require Pricebook or Item Export by default. Editing never implies export.
The adapters check the complete grant set in `Authorize`, including for empty
results, and again for each stored row reference in `Recheck`.

Options: `{"search":"","sort":"name","archived":false}`. Sort is required:
`name`, `name_desc`, or `updated`. Unknown options are rejected; search is at most
4096 UTF-8 bytes. `archived:true` includes current and archived records and
requires `item.archived.view` A even if no archived rows match. Optional
`archived_only:true` selects **only archived rows** and independently requires
that same grant, even when `archived` is false. Both flags true selects only
archived rows. Omitted `archived_only` defaults false, preserving stored options.

| archived | archived_only | Selected rows | Archived View required |
| --- | --- | --- | --- |
| false | false | Current only | No |
| true | false | Current and archived | Yes |
| false | true | Archived only | Yes |
| true | true | Archived only | Yes |

The `/items?archived=1` archived-only list should submit `archived_only:true`.
`is_active` is
retained data, not the archive discriminator (`deleted_at`).

## Fixed v1 columns, in order

- Base: `item_id,name,type,description,is_active,archived`
- Pricebook: `item_id,name,type,description,is_active,archived,sku,unit_price,taxable,tax_rate`
- Cost: `item_id,name,type,description,is_active,archived,unit_cost`

Every facet shares the persisted Item ID. Type retains `service`/`product`;
booleans use `true`/`false`. Money is read as PostgreSQL numeric text, never through
Ent's float fields. Tax-rate text is retained without conversion. CSV encoding
and spreadsheet-formula protection belong to the durable worker.

Search is literal case-insensitive containment of name or description, matching
the Item collection's searchable fields; ID and type are not searched. Each
field is matched independently, so a phrase spanning name and description does
not match.
Pricebook also searches SKU by default. Optional `search_sku:true` enables SKU
search in any facet, requiring `pricebook.view` A in addition to that facet's
normal grants. Base and Cost default to no SKU search. The flag does not add SKU,
price or tax columns to Base/Cost, and does not require Pricebook Export.
Pricebook keeps its existing SKU-search behavior when the flag is false/omitted.
The extra View grant is required even for an empty search or empty artifact;
Authorize and Snapshot enforce it, and the lifecycle rechecks stored options
before status/download. References also retain optional `search_sku:true`, so
every row recheck independently protects this search-derived authority. Omitted
flags in old options/references default false. Losing Pricebook View blocks the
whole opted-in artifact rather than silently dropping SKU-matched rows.
Name sorting is case-insensitive with ID tie-breaking (both descending for
`name_desc`); updated sorting uses descending timestamp and descending ID.
The explicitly reviewed export `name` sort uses `lower(name)`, unlike the Item
collection's raw `name ASC` order; it is not a promise to reproduce collection
case-sensitive ordering.
There is no page limit: one cursor snapshot is fetched in batches of 128 rows.
Company-owned and retained global (`company_id IS NULL`) Items match the existing
Item access boundary. Other-company rows are excluded.

References store ID, facet, snapshot archive state and optional SKU-search authority. Recheck rejects missing,
other-company, mismatched-facet or newly inaccessible archived Items. Snapshot
archive visibility is also retained, so restoring an Item cannot erase the
original artifact's archive prerequisite. Existing lifecycle code handles the
whole-result block, requester-only 24-hour Ready window, atomic audits and byte
integrity before download; adapters do not publish partial CSVs.

## Schema and retained data

Inspected migrations `004_items`, `021_company_id_multitenant`, `027_soft_delete`
and `058_item_facets`, as well as the Item Ent schema. Exports are read-only:
duplicate names remain separate IDs, SKU reservations (including archived and
previously claimed SKUs) and all Job/document line snapshots remain unchanged.
SKU claims are not extra Item rows or alternate identities.

**Item custom fields are not currently persisted:** no migration adds an Item
`custom_fields` column. Consequently these fixed Item schemas contain no custom
field column and do not fabricate empty values or historical field/option facts.
The bounded JSON requirement for masters with persisted custom fields must be
implemented by their adapters, preserving retired values rather than filtering
against active definitions. Adding actual Item custom-field support requires a
separate schema contract and a versioned export definition.

## Verification boundaries

`items_ticket97_test.go` exercises all 256 relevant grant combinations for all
three facets and every combination of `archived`, `archived_only`, and
`search_sku` (6,144 combinations), fixed column separation, option
validation, denied empty artifacts and fail-closed references. The grant matrix
tests the adapter predicate, not a fabricated authorization Principal.
Additional tests cover all archive-option combinations with and without archived
authority, invalid flag types, and backwards-compatible omitted flags.

Remaining real-PostgreSQL coverage: grant changes through production authority,
exact decimal output, duplicate/global/other-company records, more than 128 rows,
archive transitions and deleted rows during recheck, snapshot/concurrent edit
behavior, SKU reservations and line-snapshot preservation, and full durable
accept/generate/download including empty output and whole-artifact revocation.
HTTP/browser integration and repository-wide `make lint` and disposable-DB
`make test` are coordinator checks; unit success is not acceptance evidence for
those paths. No database is provisioned by this implementation slice.

Focused commands executed successfully:

```text
gofmt -w internal/exports/items.go internal/exports/items_ticket97_test.go
go test ./internal/exports -run TestTicket97Item -count=1
  ok github.com/freefsm-project/freefsm/internal/exports 0.006s
go build ./internal/exports
go vet ./internal/exports
```

These checks did not run PostgreSQL, browser tests, `make lint`, or `make test`.

### PostgreSQL cursor regression follow-up

The coordinator's existing routed `TestMasterExportsItemSnapshotFacetsHTTP`
provided a real-DB regression: sequential base/Pricebook/Cost exports, each with
260 records, failed when Pricebook reused base's cached FETCH result description:
`bind message has 6 result formats but query has 10 columns (SQLSTATE 08P01)`.
pgx caches descriptions by SQL text; the shared cursor name made FETCH text
identical despite different facet projections. Each facet now uses its own
constant cursor identifier for DECLARE, FETCH and CLOSE, giving each result
shape a distinct cache identity. No request text enters those identifiers.

Executed before and after the fix (environment values were not printed):

```bash
source /tmp/opencode/issue97-evidence-20261122/env.sh
go test ./internal/handlers -run '^TestMasterExportsItemSnapshotFacetsHTTP$' -count=1
```

Before: failed with the exact 08P01 error (1.614s). After: passed (1.647s).
The existing routed test is the regression; its file was not modified.
Also reran gofmt, focused `TestTicket97Item` tests (passed, 0.006s),
`go build ./internal/exports` and `go vet ./internal/exports` (both passed).
This targeted real-DB result does not claim the other remaining DB/browser or
repository-wide checks have passed.

### Optional SKU-search follow-up checks

After adding `search_sku`, formatting, build and vet passed again. Ran:

```text
go test ./internal/exports -run TestTicket97Item -count=1
  ok github.com/freefsm-project/freefsm/internal/exports 0.025s
source /tmp/opencode/issue97-evidence-20261122/env.sh
go test ./internal/handlers -run '^TestMasterExportsItemSnapshotFacetsHTTP$' -count=1
  ok github.com/freefsm-project/freefsm/internal/handlers 1.814s
```

The existing routed test covers the default facet snapshot path. The follow-up
below adds real-DB opt-in selection and revocation coverage. The focused unit matrix checks all option/grant combinations
and optional reference serialization/backwards compatibility.

### Real-DB SKU authority and exact decimals

Added only the uniquely named
`internal/handlers/items_ticket97_lifecycle_integration_test.go`, reusing
`permissionHTTPFixture` without modifying shared fixtures/tests. Every case
receives its own migrated schema and cleanup; no shared records are changed.
These are durable-service/adapter integration tests in the handlers test package,
not new routed HTTP/browser tests.

`TestTicket97ItemSKUSearchDurableAuthority` covers all three facets, each with
empty and populated output:

- Missing Pricebook View rejects acceptance without persisting an export job.
- Snapshot independently rejects a real database-loaded Principal lacking it.
- Base/Cost without opt-in work without Pricebook View and do not match SKU-only
  terms; with opt-in and the grant, they match the Item by SKU.
- Ready CSV downloads succeed before revocation, including header-only output.
- Stored populated-row references retain `search_sku:true`; direct row recheck
  rejects revoked authority.
- After revocation, PrepareDownload withholds the whole artifact for empty and
  populated results; status is blocked with counts/as-of withheld. Denied
  downloads add no download audit.

`TestTicket97ItemExactNumericDurableFacets` inserts numeric strings directly into
SQL, then runs acceptance, generation, status and CSV download for all facets.
It verifies identical persisted IDs/order for duplicate names, fixed columns and
facet exclusion, retained SKU/tax-rate text, and exact monetary strings including
`0.00`, `0.01`, `0.10`, `9999999999.99` (NUMERIC(12,2) maximum),
`1234567890.12`, and `9876543210.98`. Neither seeding nor expected values uses floats.

Executed with the disposable database environment (no secrets printed):

```bash
source /tmp/opencode/issue97-evidence-20261122/env.sh
go test ./internal/handlers -run '^TestTicket97Item(SKUSearchDurableAuthority|ExactNumericDurableFacets)$' -count=1 -v
```

Result: all six authority subtests and the exact-decimal test passed, total
8.873s; no integration skips. Also passed:

```text
gofmt -w internal/handlers/items_ticket97_lifecycle_integration_test.go
go test ./internal/exports -run TestTicket97Item -count=1  # 0.045s
go build ./internal/exports ./internal/handlers
go vet ./internal/exports ./internal/handlers
```

Remaining scope: new `search_sku` routed-form/browser transport coverage,
concurrent revocation timing, and the coordinator's full repository lint/test
checks. This follow-up does not claim new coverage for those paths or for the
other archive/snapshot-preservation cases listed above.

### Collection search alignment follow-up (database checks paused)

Search now matches name/description independently, with authorized SKU behavior
unchanged; it no longer searches ID/type or concatenates fields. Added
`TestTicket97ItemSearchMatchesCollectionFields` in the owned integration file:
all three facets, both SKU flags, ID/type nonmatches, cross-field phrase nonmatch,
case-insensitive name/description matches and conditional SKU matches.
This new database test has **not been run**, pending the coordinator's restart
notification. Earlier DB evidence above predates this search change.

Executed only formatting, unit tests and compile/static checks:

```text
gofmt -w internal/exports/items.go internal/exports/items_ticket97_test.go internal/handlers/items_ticket97_lifecycle_integration_test.go
go test ./internal/exports -run '^TestTicket97Item' -count=1  # passed, 0.032s
go build ./internal/exports ./internal/handlers              # passed
go vet ./internal/exports ./internal/handlers                # passed
```

No database tests were run during this follow-up. Export review keeps the
case-insensitive `lower(name)` sort, explicitly distinct from raw collection
`name ASC` ordering.

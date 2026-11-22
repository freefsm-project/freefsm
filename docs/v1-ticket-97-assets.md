# Asset and Service Entry CSV contracts

Both adapters use the existing durable export engine: requester-only artifacts,
one repeatable-read snapshot, all matching rows, 24-hour Ready availability,
and whole-artifact current-authority rechecks. Nested values are JSON in a CSV
cell; timestamps are RFC3339 with fractional seconds where recorded. Empty
optional scalar cells mean no recorded value. JSON unknown dates remain `null`.

## Asset

Register `exports.Asset{}` (`asset-operational-v1`). Requires `asset.export(A)`
and `asset.view(A)`. Options: `search` (Number/name/serial/manufacturer/
model substring, case-insensitive), `sort` (`name` default, `name_desc`,
`number`, `number_desc`, `updated`), `archived` (default false; true includes archived
and requires `asset.archived.view(A)`). Sorts have stable ID tie-breakers.

`archived_only:true` requires `archived:true` and selects only archived Assets,
matching the archived Asset list. Without `archived_only`, `archived:true`
includes active and archived Assets. `archived_only:true` with `archived:false`
or omitted is a validation conflict. This restriction applies to Assets, not
their selected Customer context: an active selected Customer remains valid.

Optional `customer_id`, `asset_type_id`, `asset_status_id` are nonnegative
integer IDs (0 means no filter), combined with AND and matching current Asset
relationships. Negative IDs are rejected. Selected Type/Status IDs must belong
to the company or be global; retained retired selections remain valid.
Selecting a Customer additionally requires `customer.view(A)` and a currently
visible same-company Customer, even if no Assets match. An archived selected
Customer requires `archived:true` and `customer.archived.view(A)`. Selection
authority is checked at acceptance, generation, status and download, including
empty exports; this is separate from bounded historical labels in output rows.

Fixed headers, in order:

```text
asset_id,number,name,ownership_class,association_kind,customer_id,customer,location_id,location,type_id,type,status_id,status,serial_number,manufacturer,model,installed_at,warranty_expires,service_notes,custom_fields,archived,association_periods,association_events
```

`association_kind` is `owner` for Customer-owned equipment and `placement` for
Company-owned equipment, including unplaced equipment. Number and ownership
class are retained identities. Type/Status labels include retained retired
values. `custom_fields` retains the stored values, including retired fields.

Current references and association periods follow `AssetAssociationHistory`:
Customer IDs/labels independently require `customer.view(A)`; Location IDs/labels
require `location.view(A)`. Otherwise the label is Restricted Customer/Location
and the ID is absent. A missing Customer is Unplaced. Archived labels carry
` (Archived)` without granting detail access. No Job relationship is expanded.

`association_periods` objects contain `event_id`, `customer_id`, `customer`,
`location_id`, `location`, `starts_on`, `ends_before`. Unknown initial starts and
open ends are null, not invented acquisition dates. `association_events`
objects contain `id`, `event_id`, `action`, `effective_date`, `reason`,
`recorded_at`; origin revisions use event ID 0. Events include corrections.
Raw `before_fact` and origin `evidence` are never exported.

Stored references are `{id, archived, customer, location}`. The last two flags
mean an optional authorized reference was actually disclosed anywhere in the
row. Losing such a grant blocks the whole artifact; losing an unused grant does
not. Rechecks require current Asset existence/company/visibility, archived
authority for originally archived and currently archived Assets, and Export.

## Service Entry

Register `exports.ServiceEntry{}` (`service-entry-v1`). Requires
`service_entry.export(A)`, `service_entry.view(A)` and `asset.view(A)`.
Assigned Job Context cannot export lifetime history. No Job, Customer or
directory View grants are prerequisites. Options: `asset_id` (optional positive
selected Asset, 0 means all), `search` (entry ID/Asset Number/name/Job title/current
summary substring), `sort` (`date` default, `date_desc`, `asset`, `updated`),
`archived` (same explicit Asset archive policy as above). All sorts have stable
identity tie-breakers. Selected Assets are authorized even with no entries.

Fixed headers, in order:

```text
service_entry_id,asset_id,asset_number,asset_name,asset_archived,job_id,job_title,service_date,summary,responsible_users,revision_id,revised_at,actor_id,actor,accepted_at,revisions
```

Entry actor/accepted time describe original creation; revision ID/time and
summary describe the current terminal revision. `responsible_users` is an
array of `{id,name}`. `revisions` is an array of explicit objects:
`id`, `predecessor` (null initially), `service_date`, `summary`,
`responsible_users`, `actor` (`{id,name}`), `accepted_at`. No revision reason is
invented. Names and bounded Job titles are resolved at export snapshot time,
including renamed and disabled participants; they are not claimed to be names
recorded at service time. Service Entries have no Custom Field storage.

Lifetime evidence survives unlinking, transfer and contributing Job archive;
the operational `serviceEntryRelationship` check is intentionally inapplicable.
Only bounded Job ID/title and participant identities are included, never Job
details or Customer references. Stored references are `{id, asset_id, archived}`.
Download rechecks current entry identity/company and current Asset visibility,
plus the original archive facet and all mandatory grants.

## Focused PostgreSQL verification

```sh
FREEFSM_TEST_DATABASE_URL='<disposable PostgreSQL URL>' go test ./internal/handlers -run 'Test(AssetExportBounded|AssetExportListFilters|AssetExportTransferCorrection|ServiceEntryExportLifetime)' -count=1 -v
make lint
FREEFSM_TEST_DATABASE_URL='<disposable PostgreSQL URL>' make test
```

The populated `TestAssetExportBoundedAssociationsAndDisclosureRevocationHTTP`
and `TestServiceEntryExportLifetimeRevisionsAndAssetRecheckHTTP` use the migrated
production-router fixture (`handlers.New`, production middleware, persisted
browser sessions). They GET the review and POST acceptance through
`/assets/export` and `/service-entries/export`, run `exports.Product.ProcessOne`,
verify Pending/Ready status and persisted data-row counts, then GET
`/exports/{id}/download`. Assertions parse the actual CSV and its nested history
JSON, including the fixed headers. Revoked Customer disclosure and newly
archived Asset history each return HTTP 410 for the existing populated artifact;
the restricted Asset artifact remains downloadable after loss of an unused
Customer grant. Direct Snapshot/Recheck checks remain supplemental.

`TestAssetExportTransferCorrectionArchivedLabelsAndHistoricalJobHTTP` submits
a real transfer and its correction through authenticated association routes.
It verifies that the exported period uses the corrected Location but retains
the original event identity, while both immutable event revisions retain their
own IDs, actions, reasons and timestamps. Archived former Customer/Location
labels are checked against the existing association-history page with only
ordinary View grants; removing those grants removes their IDs and labels from
the CSV and page. The original assigned Job's stored association evidence stays
unchanged and becomes non-operational, its authenticated master-context response
does not disclose destination data, and Assigned/Context authority cannot open
association history or export lifetime Asset information. Each test uses its
own migrated PostgreSQL schema and cleans up only that schema.

Together the focused tests cover independent disclosure/revocation, placement semantics,
unknown origin dates and corrections, retained values, lifetime evidence after
unlink and Job archive, disabled/renamed participant names, predecessor chains,
Asset archive rechecks and denial of Assigned-only history authority. Filter
coverage verifies combined Customer/Type/Status selection, default name order,
negative ID rejection, and empty-result revocation at status and download.
Archived filtering verifies active-only, inclusive and archived-only output,
including rejection of archived-only options without explicit archived intent.

# Captured Job pricing (#44)

## Authority and workflow

Create the minimal Job, then use **Edit Job → Line Items** to capture an Item or
enter free text. This is office planning: `job.edit/A` and independently scoped
`pricing.view` on that Job are required. Operational Job View alone does not
expose captured pricing, Billing Type, editor JSON or catalog choices. A planner
without pricing View can save other details without resubmitting or clearing
pricing. Item base/Pricebook View are independent and required only for new
catalog selections; existing captures remain readable/editable with Job pricing
authority after catalog access is removed.

The editor copies authorized name, description, price, taxable flag and tax rate.
Quantity, descriptive and pricing values remain editable. Catalog Cost is never
in the bootstrap, ordinary Job output, public field projection or line payload.
Unknown line fields (including `unit_cost`) reject rather than becoming stored
pricing. Captured tax rates are per line; empty historical rates remain empty.

Each accepted line has a stable `snapshot_id`. Its optional `item_id` is retained
provenance, not a live catalog binding. Edit the captured values to negotiate a
price; to select another Item, remove the old capture and add a new line. A
retained ID cannot be duplicated, forged or retargeted, including converting a
historical free-text line into a claimed Item selection. Adding a new line for
the same Item is still a new selection, even when another capture is removed in
the same request.

New selection accepts only an active Item with current Item/Pricebook authority.
Catalog price, tax, description or archive changes never reprice old captures.
Price drift while accepting an explicitly captured value neither changes that
value nor invalidates it. Archive or grant/session loss rejects the affected
write. Recurrence and existing Job-to-document copy commands recheck source
pricing authority and carry snapshots without requiring historical Items to be
active. They do not manufacture source Items for free-text entries.

The existing save identity/revision protects Job details. The shared committing
transaction fences grants, sessions, parent scope and catalog state; it commits
the effect with protected `job.edit`/`job.create`, captured-pricing facet and new
Item-selection audit. An audit failure rolls back the entire edit. Operational
activity gets the existing nonsensitive Job event summary, never catalog Cost.

## Public field API and reference harness

`GET /api/v1/jobs` and `GET /api/v1/jobs/{id}` include `line_items` and
`billing_type` only with current parent-bounded `pricing.view`. Their absence
means the facet is unavailable, not a zero-priced Job. Job View and pricing scopes
are intersected independently, regardless of recorded legacy Role labels.
Customer-context responses include neither captured pricing nor catalog Items.

Item lookup/selection remains the bounded Cost-free API established in #43.
`internal/api/v1/testdata/job-pricing-reference-harness.cjs` reads an allowlisted
captured-pricing projection; the Item reference harness supports selection for
the commercial field workflows. Looking up a current Item never reprices a Job
capture. The conformance runner exercises real bearer HTTP, rejects Job pricing
planning/catalog-maintenance mutations, and purges cached data on revocation.
There is no Job pricing mutation endpoint. Offline receipt/lease protocols remain
the owning API slices; staging is never claimed as an accepted server effect.

## Non-destructive upgrade

Migration `076_job_pricing` is a transactional review preflight. It changes no
Job, Item, relationship, captured price, provenance, Role or grant. Known legacy
captures are read without repricing. A missing line identity gets a deterministic
Job/position identifier in the read projection, persisted only by an explicit
pricing save. That identifier makes no assertion about the historical selection's
date or actor. Missing Item provenance stays absent/zero.

Unknown historical fields, unsupported line shapes, invalid recorded quantities,
prices/tax, or ambiguous line identities stop cutover with the Job ID and leave
the original data untouched. Before retrying:

1. Retain the pre-upgrade backup and original affected rows.
2. Have an authenticated product Administrator review the actual recorded facts
   and explicitly approve any mapping or correction in the installation's change
   record. Host authority and legacy Role names are not product approval.
3. Apply only that reviewed repair through the prior installation's supported
   actions or a separately reviewed procedure. Test the upgrade on a disposable
   copy. If the meaning remains unknown or review is missing, keep cutover stopped.

No automatic mapping silently drops unknown historical Cost or invents a source
Item. No nonprotected Role receives new grants. Rollback uses the prior binary
and its matching backup.

## Acceptance seams

- `internal/handlers/job_pricing_integration_test.go`: production HTTP capture,
  independent grants, malformed/Cost input denial, stable provenance, catalog
  drift/archive, atomic audit failure, waiting revoke/archive/drift, session
  expiry at acceptance, assigned field API/context nondisclosure, executable
  reference harness and persisted 320px Chromium capture/edit.
- `internal/database/job_pricing_upgrade_integration_test.go`: rejected unknown
  legacy shape leaves all facts unchanged; explicitly reviewed fixture repair
  permits upgrade while retaining snapshots, identifiers and grants.
- Existing Item selection/copy tests continue to exercise all pricing writers and
  historical captures. Local command results are recorded in the ticket's durable
  implementation result, rather than asserting unexecuted release-wide coverage.

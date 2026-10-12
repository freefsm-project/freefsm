# v1 integration: checkpoints 20–46

This integration merges checkpoints #20, #24, #25, #26, #27, #28, #29, #31,
#43, #45 and #46. It is **not full ticket acceptance or whole-v1 completion**.
The individual domain documents retain their downstream ownership boundaries.

## Composition repairs

- Document creation joins the typed-details transaction. Parent authority,
  required fields, Item selection, number allocation, persistence and immutable
  audits commit or roll back together. Catalog selection uses the shared Item
  policy, including shared Items, occurrence counting and explicit copy context.
- Job Move acquires the authority fence before the Job row, matching document
  creation/PDF commands. The pgx/Ent adapter preserves database trigger errors.
- Customer error forms retain submitted revisions and issue new action identities.
  Receipt-enabled Customer forms retain enhanced submission and lost-response
  reconciliation. Relationship search retains URL sorting/pagination controls.
- The selected-PDF composer uses captured source values for HTML defaults, retains
  submitted HTML, and freezes it with the selected bytes and sender. Generated
  PDF permissions remain independent of collaboration File Upload.
- Delivery captures record and meaning revisions. Acceptance shares workflow
  configuration's fence, locks/revalidates destinations, and skips automation for
  stale/unknown intent while recording accepted transmission. Retryable acceptance
  persistence failures retry without sending the already-accepted message again.
  This is not an exactly-once SMTP guarantee.
- Item activity discovery honors Item View rather than legacy office/protected
  Administrator status. Existing facet-filtered projections remain in force.

## Upgrade preservation

Migration identities are full filenames, not numeric prefixes. All checkpoint
identities remain intact. New migration `068_integration_identity_master_guards`
permits narrowly verified legacy-number review and same-category historical
reference replacement without disabling conversion tombstone/master guards.
`069_delivery_workflow_intent` adds immutable nullable revision evidence; it does
not fabricate historical intent. Neither migration adds Role grants.

The workflow preflight can read missing legacy archive columns without changing
data. The older pre-032 workflow inventory gap remains: an intervening migration
may introduce a definition absent from the initial review. The tested older
upgrade uses explicit staged review through migration 043; one-shot upgrade from
every historical release is not claimed.

## Verification

On PostgreSQL 18 at `127.0.0.1:55419`, isolated UTF8 database
`freefsm_v1_integration_verified_18`, with `GOMAXPROCS=2 GOFLAGS=-p=2`:

- `make generate`, `make lint`, and explicit database-enabled `make test` passed.
  Backup integration used the disposable cluster's `postgres` admin database.
- Combined race-enabled persisted Chromium journeys passed for every merged
  area, Customer permissions, executable installation/recovery and host recovery.
  All 32 document-line browser cases passed. Browser executable:
  `/usr/bin/chromium-browser`; dependency directory supplied read-only via
  `NODE_PATH` from the original checkout.
- New production/database tests cover composed creation/audit rollback, hidden
  numbering review, historical master replacement, Customer error revisions,
  selected-PDF HTML, workflow ABA/destination races and acceptance retry.

The initial suite deadlocked and was interrupted; a subsequent full run found
additional fixture/adapter defects. Those unsuccessful runs are not passes.
Unsupported PostgreSQL <16, nginx/1-GiB scale, native reboot/release matrices and
the optional intercepted backup browser were not established by these checks.

## Remaining acceptance defects and gaps

- **#24:** complete permission-aware navigation, existing-workflow receipts and
  failure UX, measured accessibility/contrast/native zoom, and desktop/mobile
  browser matrix remain incomplete.
- **Delivery handoff:** ordinary creation's legacy route mismatch is resolved and
  equal-grant `tech` users are tested. Manual Delivery retry still uses inherited
  legacy authority and lacks the independent current-grant/protected-audit
  boundary of initial queueing; it remains unresolved.
- **#27:** new intent discovery still uses legacy Job access and separate reads
  can pair presentation with a newer meaning token. The pre-032 inventory gap
  above also remains. Acceptance's separate revision/fence defects are repaired.
- **#28:** selecting "No Status" on an existing Asset does not clear its Status.
  `internal/handlers/assets.go` maps the submitted zero to a nil pointer, while
  `internal/services/asset.go` treats nil as unchanged and requires a nonnil zero
  to clear. The #29 adapter does provide effective authority and atomic protected
  `asset.edit` audit, including the required actor/Role/action/scope/target/time/
  channel/outcome. The earlier claim that this audit boundary was incomplete is
  withdrawn: a complete Type/Status diff or atomic ordinary feed entry is not an
  additional AUTH-04 acceptance requirement for this command.
- **#31:** existing child-only Contact/Location grants still additionally require
  Customer View. Customer-owned Asset cascade and Lease blockers remain owning
  slice handoffs; the conservative Asset blocker is not canonical cascade proof.
- **#46:** API mutation audit lacks complete device/operation attribution; valid
  legacy DOC/XLS/PPT corpus journeys and Lease wiring remain unproven/unimplemented.
  Authenticated generated PDFs do not have the standalone File Upload coupling.

## Coordinator closure assessment

No tickets were closed. The following recommendations combine checkpoint evidence,
the merged production/database/browser checks and independent source review:

| Ticket | Recommendation | Evidence or blocking gap |
|---|---|---|
| #20 | Eligible for scoped closure | Role/account/directory HTTP, concurrency, audit and persisted browser checks pass. |
| #24 | Keep open | Existing-workflow interaction/navigation and accessibility/browser matrix gaps above. |
| #25 | Eligible for scoped closure | Equal-grant creation, atomic numbering/fields/Item selection, hidden-number review, future defaults and independent immutable PDF checks pass. Manual Delivery retry remains a separate unresolved Delivery handoff. |
| #26 | Eligible for scoped closure | Encrypted settings, independent grants, selected HTML/PDF/sender, current transport, coordinates and persisted browser checks pass. |
| #27 | Keep open | Intent-discovery authorization/coherent snapshot and pre-032 reviewed-upgrade gaps above. |
| #28 | Keep open pending bounded correction | Atomic authorization/audit is supported; the offered No Status selection silently retains an existing Status. |
| #29 | Eligible for scoped closure | Typed-field validation, whole-command rollback, composed creation and browser correction/default journeys pass. |
| #31 | Keep open | Existing child-only grants still require unrelated Customer View. |
| #43 | Eligible for scoped closure | Facets, shared Item selection, captures, current authority, atomic audit, upgrade and persisted browser checks pass. |
| #45 | Eligible for available-parent closure | Thread/removal/receipts, denial, concurrency, conversion and persisted browser checks pass. |
| #46 | Keep open | API device/operation attribution and representative legacy Office format evidence remain outstanding. |

Lease, Asset ownership/cascade, broader Delivery/offline domains and unexecuted
release/platform gates retain their explicit owning-ticket handoffs. No closure
recommendation asserts completion of those domains or certifies every historical
checkpoint database upgrade.

The coordinator additionally ran `go test -race ./internal/handlers -run
'^TestWorkflow' -count=1 -v` with `BROWSER_TESTS=1`, the same Chromium/dependency
settings, and independently provisioned UTF8 database `freefsm_v1_merger_review_18`
on port 55419. All three selected tests passed, including the embedded persisted
workflow browser journey (4.623s package total).

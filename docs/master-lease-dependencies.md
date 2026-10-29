# Master archive dependency contract

Contact, Location and Asset lifecycle commands use the existing authorization
transaction fence (`pg_advisory_xact_lock(190019)`). Database statement triggers
place legacy master/work-link writers under the same fence. Eligibility uses
semantic Job/Project categories, never progress, display labels or Asset Status.
Primary Contact/Location archive clears only that record's default; the command's
immutable before/after audit records the actual change. Restore cannot reinstate
the former primary or replace the current one.

## Lease registration and finalization seam

This installation has Lease permissions and number configuration but no Lease
agreement/coverage store. Migration 103 introduces **dependency projections**, not
agreements, billing, placement events or reconstructed historical coverage:

- `lease_master_dependencies`: stable Lease/Company/Customer identity, optional
  premises, semantic lifecycle and archive state.
- `lease_asset_dependencies`: explicit Asset coverage start/end dates supplied by
  the owning Lease writer. End dates are inclusive; NULL means no recorded end.

Application registration/finalization writers call
`authorization.Service.WriteLeaseDependencies(ctx, tx, actor, snapshot)` in the
**same transaction** as their Lease effect. The hook acquires the shared fence,
checks current identity and independent `lease.manage` authority, rejects field
API maintenance, validates the whole snapshot, and writes immutable before/after
audit. Changing archive state additionally requires the independent Archive or
Restore grant, with archived View for an archived source; Manage implies neither.
This is the registration/finalization adapter. A future archive-only command must
use the same SQL seam under its own independent lifecycle permission and audit,
rather than requiring Manage by routing through this adapter.
The caller must roll back on any error and owns commit. It must also enforce
the agreement's own rules (term, at-least-one-Asset, overlap, billing, transition
and event history); this hook does not replace them or commit independently.

New selections are independently authorized inside that same fence. A new
Customer requires All Records Customer View; a changed/nonempty premises requires
All Records Location View; a new coverage key `(Asset ID, starts_on)` requires All
Records Asset View. New targets must be active and belong to the actor's Company
(and premises to the selected Customer). Lease Manage, a known ID, or Assigned Job
Context does not substitute for any of those grants. An existing foreign-Company
Lease ID is not a usable retained-reference anchor. Dates supplied to the adapter
are ISO company-calendar dates, with NULL for an open coverage end.

Retaining an existing Customer/premises/coverage key or removing a reference does
not reacquire selection authority. Revising the end of a retained coverage key
keeps that selection; moving its start or re-adding a removed interval is a new
selection. Every retained reference still undergoes domain validation: this
exception does not finalize stale archived selections or bypass placement rules.
Native Drafts store selected Assets separately from coverage. Opening their first
open-ended commencement coverage at the saved Draft start retains those selections
without Asset View; the adapter verifies the saved selection, Customer, premises
and Draft lifecycle under the same fence. New Assets and changed coverage starts
still require Asset View, and all coverage undergoes the same domain validation.
Terminated retained history may reference archived masters without reviving them.

The database seam `set_lease_master_dependencies` is available to transactional
SQL writers. Like other direct SQL writers, those callers own actor authorization
and audit; web/field callers must not invoke it as an unaudited command. Projection
table triggers enforce dependency invariants even for direct INSERT/UPDATE and
reject deletion of Lease identity. The snapshot function supports exact audit and
inspection. The projection is replaced atomically, retaining unchanged coverage
keys; it is not an event ledger and cannot stand in for Lease history.

- Unarchived Draft/Scheduled/Active/Month-to-Month premises block Location archive.
- Scheduled/Active/Month-to-Month coverage with an inclusive end today or later,
  or no end, blocks Asset archive, including future starts. Company-local today
  uses the existing company timezone.
- Draft coverage reserves no Asset. New Draft Asset selection requires an active
  Asset; a retained stale Draft selection can remain but must be repaired before
  finalization creates current/future coverage.
- Terminated, archived and entirely past coverage do not block Asset archive.
  Only Draft/Terminated dependency snapshots can be archived.
- Operational Lease snapshots require an active Customer and same-Customer
  active premises. Coverage uses Company-owned Assets; current/future finalized
  coverage requires active Assets that are unplaced or placed with that Customer.
- Placement with another Customer and conflicting coverage serialize under the
  same fence and reject in either order. Return does not remove coverage;
  termination does not return equipment. A placed Company-owned Asset must still
  be explicitly returned before archive.
- Unarchived non-Terminated Lease dependency snapshots also block Customer archive
  and its cascade preview.

## Whole placement timeline (migration 104)

`lease_asset_timeline_validate` is shared by registration/replacement and the
existing association/correction command, including initial-observation factual
revisions. It checks overlap between **every recorded finalized** coverage interval,
including retained Terminated and archived history, and each differently placed
Customer period, not just `assets.customer_id` today. A return that leaves the
Asset unplaced today cannot hide a conflicting earlier placement.

Association periods are `[starts_on, ends_before)`; coverage is
`[starts_on, ends_on]` with an inclusive final company-calendar day. Coverage
starting on a return day does not overlap the placement that ends before that
day. Coverage ending on a placement's start day does overlap. Same-day empty
placement periods do not conflict. Unknown initial starts remain NULL: if the
known return is at or before commencement, the observation is disjoint. Otherwise
a different Customer's unknown-start observation requires explicit mapping review;
it is not asserted to be an infinite historical placement. Unplaced periods do
not conflict. A Terminated coverage row with no known end likewise requires
explicit mapping review rather than an invented end or infinite coverage.

This timeline consistency rule is distinct from archive eligibility: wholly past
coverage and Terminated/archived references do not block ordinary master archive.
They do protect overlapping historical placements. Draft intended selections
reserve no coverage; non-overlapping historical corrections and new placement
after the inclusive coverage end remain valid.
Original association facts remain immutable; accepted corrections append a new
revision and their actual audit rather than rewriting old dates/owners.

The commands validate before their audit. Deferred database constraint triggers
also enforce the same invariant on direct Lease projection and association-event
SQL, evaluating final transaction state for multi-row repairs. Projection
eligibility is also validated against the final lifecycle, so replacing an
Active projection with Terminated references does not accidentally validate the
new references against transient old Active metadata. Immutable identity checks
remain immediate. New Draft coverage still requires an active Asset at commit;
retained stale Draft keys are not treated as new selections. Statement fences
on association events/origin revisions use the same 190019 owner as the Lease
projection writers. Migration 104 adds guards only; it does not rewrite existing
facts, synthesize coverage, fill unknown starts or change grants. Migration 112
extends that shared historical validator to retained finalized intervals and
preflights existing incompatible facts for explicit mapping review. It changes
neither archive eligibility functions nor facts, dates or permissions.

No rows are backfilled, no dates are inferred from Asset placement, and no grants
are added. If an installed database already contains `leases`, `lease_assets` or
`lease_coverage`, migration stops with an explicit mapping-review error instead
of silently interpreting an unknown schema as empty or manufacturing facts. An
installation-specific mapping must preserve the original data and establish the
explicit semantic state/dates before this migration is adopted. The eventual
native Lease store must consume this seam in every registration, finalization,
coverage, premises, lifecycle and archive writer, not merely in a UI preview.

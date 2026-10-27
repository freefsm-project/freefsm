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

No rows are backfilled, no dates are inferred from Asset placement, and no grants
are added. If an installed database already contains `leases`, `lease_assets` or
`lease_coverage`, migration stops with an explicit mapping-review error instead
of silently interpreting an unknown schema as empty or manufacturing facts. An
installation-specific mapping must preserve the original data and establish the
explicit semantic state/dates before this migration is adopted. The eventual
native Lease store must consume this seam in every registration, finalization,
coverage, premises, lifecycle and archive writer, not merely in a UI preview.

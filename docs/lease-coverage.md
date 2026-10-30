# Effective-dated Lease Asset coverage

Finalized Lease details link to **Coverage changes**. Office users can Add,
Remove, or atomically Replace Assets on historical, current, or future company
dates. Every change requires a reason. Pending events can be revised or withdrawn;
effective events allow factual date correction only. A genuine later change is a
new event. The page retains the complete revision history and inclusive coverage
intervals. Unplaced and wrong-premises selections are labelled; coverage never
changes physical placement.

## Authority and persistence

The browser writer requires Lease View/Manage and Billing View/Manage. Selecting
a new Asset independently requires Asset View and active Company ownership.
Invoice Create is not required for bounded ordinary generation; subsequent Invoice
operations retain their independent permissions. The public field API has no
coverage-management route, including for broadly authorized mobile users.

Migration 120 adds empty append-only event and revision stores. It changes no
recorded agreement, identifier, permission grant, or historical membership fact.
Revisions retain the actor, reason, effective action, submitted intent, immutable
operation identity, and acceptance timestamp. Exact replay returns the original
event; changed intent or stale whole-timeline revision rejects. Audit, source-review
flags, and the dependency projection commit together under the existing shared
authority/master fence (190019).

The full event timeline is replayed from recorded finalization allocations.
Removing the last Asset is rejected, including at future boundaries. Replacement
is one event, ending outgoing coverage the preceding day and beginning incoming
coverage on its effective date. A zero-day interval is omitted from the projection
without deleting its events. Existing inclusive cross-Lease overlap and retained
placement constraints run in the same transaction. A deferred completeness guard
also protects native event-managed coverage against later dependency writers.

Existing coverage that cannot be reproduced from recorded finalization and event
history is retained and requires explicit source mapping review; it is never
silently reconstructed. Known Terminated dependency intervals provide their
inclusive final coverage date. The fixed-term end is not treated as termination:
the agreement continues month-to-month.

## Billing

Membership and billing entitlement are separate projections of the same events.
Add begins recurring rent next month and accepts an optional explicit one-time
charge. Remove retains the current month's allocation. Replace carries the
outgoing recurring rate and tax treatment, retaining its current-month provenance
and starting incoming billing next month, without a default extra charge. Exact
amount/tax validation uses the ordinary Invoice calculator and Customer exemption.

The existing durable worker and reviewed-retry path share current-source term
selection. Optional Add charges use a `coverage` obligation grouped by Lease and
effective due date, obeying the managed-billing cutoff and 30-day creation window.
Generated Invoices are never rewritten. Source changes flag affected generated
documents for manual review, and ordinary void/replacement suppression remains in
force. Ungenerated obligations reconcile through the existing immutable revision
mechanism.

## Regression seams

- `TestLeaseCoverageTimeline` and `TestLeaseCoverageBillingMonthBoundary`:
  complete membership replay, inclusive boundaries, future gaps, invalid later
  references, re-addition, agreement bounds and first-day billing treatment.
- `TestLeaseCoverageHTTP`: production writes, replay, last-Asset rejection,
  grant-loss waits, late audit failure, invoice immutability, field-channel denial,
  competing replacements and master archive blockers. Its opt-in Chromium subtest
  performs Add/Remove/Replace, pending revise/withdraw and factual correction on a
  phone viewport with retained history.
- `TestLeaseCoverageBillingHTTP`: real generated monthly amounts and Asset
  provenance, rate carry-forward, removal, explicit Add charge and worker replay.
- `TestLeaseCoveragePopulatedUpgrade` and `TestLeaseCoverageEncryptedRestore`:
  populated predecessor preservation, no invented events, migration replay,
  encrypted event/projection/finance recovery and restored operation replay.

The original Lease placement/finalization, billing, denial, concurrency, native
upgrade and encrypted-restore tests remain required preservation roots. Full
qualification needs the explicit disposable PostgreSQL URL, backup/admin fixtures,
browser fixtures, uncached race execution and separate accounting for test skips
versus packages without tests.

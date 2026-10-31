# Effective Lease billing revisions

From **Leases — Billing**, open a finalized Lease and choose **Revise billing and
view effective terms**. Billing View and Billing Manage are independent of base
Lease permissions. This browser-only command grants no Invoice viewing, editing,
finalization, sending, or settlement authority.

Select each affected Asset, enter its complete new monthly rent in USD cents and
tax percentage, and supply an effective date and reason. Multiple Asset changes
are accepted together so a common taxable rate can change atomically. Zero-tax
allocations remain non-taxable; Customer exemption takes precedence over valid
stored rates. The existing exact commercial calculator validates every affected
ungenerated configuration, including scheduled coverage and later rate changes.
There is no blended rate or Lease-specific financial ledger.

## Effective history and generation

Migration 123 adds empty, immutable billing revision history. Each event retains
the actor, acceptance timestamp, effective date, reason, original submission
identity, and per-Asset before/after rent and tax. No installed facts, grants,
identifiers, finalization snapshots, or initial charge amounts are rewritten.

Monthly charges use the applicable terms on their first-of-month due date, with
no automatic proration. Coverage and billing events replay in effective order;
replacement carries the outgoing rate in effect at the replacement event.
Revisions do not change initial due, corrected commencement, the managed billing
cutoff, or the 30-calendar-day generation window. Initial and optional coverage
charges retain their approved amount; applicable revised tax may affect an
ungenerated charge. Generated line provenance identifies the governing billing
revision where applicable, and the period page links to its immutable history.

An allocation begins with initial terms, an Add, or a Replace. Removing and
re-adding the same Asset starts a new explicit allocation: an older allocation's
tax revision cannot override the new Add's optional charge or its provenance.
Optional Add charges on one date remain grouped in one obligation; monthly and
one-time charge resolution share the same effective-event replay.
Each optional charge belongs to its Add event, even if a same-day Remove or
Replace ends that allocation. Re-adding the same Asset on that date retains each
explicit charge and its own tax basis; it does not merge or overwrite the earlier
charge. Recurring billing separately follows the surviving allocation next month.

Pending coverage revision, withdrawal, and factual date correction must keep
every approved billing revision attached to its original allocation. A change
that moves the incoming Add/Replace to or beyond that revision's effective date,
or changes which allocation it would revise, rejects atomically with the revision,
Asset and date to review. Coverage on the exact billing date still belongs to the
outgoing monthly allocation, so moving an incoming event to that date does not
silently transfer its approved revision. The retained browser proposal can be
corrected to a compatible date or discarded; accepted history is never reset.

Already generated Draft, finalized, Void, and settled Invoices remain snapshots.
Source-change flags identify generated periods for explicit manual adjustment or
void under the ordinary Invoice contract. A July Draft generated June 1 remains
an exception after June 10 termination effective June 30; future unowed periods
stop. Voiding never automatically recreates an obligation.

## Atomicity and recovery

The same current-principal/master write fence serializes revisions with generation,
coverage, placement, archive, and authorization changes. Acceptance checks current
Lease lifecycle, active Customer/premises/selected Assets, and coverage-placement
invariants. Revision history, source-change flags and audit commit together;
failed acceptance leaves none of those effects. Generation takes one coherent
applicable set of terms and retains its existing durable retry identity.

Same-intent retries return the accepted result; changed intent under the same
identity or a stale billing revision rejects. Validation and confirmed rollback
retain the proposal. Stale recovery displays saved history and offers explicit
revise-against-current or discard; refreshing the revision never saves. Uncertain
outcomes permit only exact-submission retry. Recovery reloads current authority
before reflecting submitted billing values. Archived Leases expose authorized
history but reject new revisions.

## Qualification

Owning production PostgreSQL/HTTP tests cover revision persistence, independent
authority and field-API-channel denial, queued grant loss, rollback, immutable
history, both generation/revision commit orders, stale competing submissions,
effective replacement-rate inheritance, tax schedule compatibility, generated
Invoice and settlement preservation, July termination exceptions, archive denial,
populated upgrade and encrypted cross-instance restore/replay. A real Chromium
journey covers normal mobile navigation, retained validation and stale
compare/revise/discard. Durable command, source and log inventories are under
`.agent-state/v1-spec-18/ticket-71`; full qualification and independent acceptance
are separate parent-owned gates.

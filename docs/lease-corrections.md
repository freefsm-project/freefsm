# Lease factual corrections

The Lease detail page links to **Correct agreement facts** and **Notice and
termination**. The latter includes **Correct factual termination**, including
ended and cancelled agreements. These are browser-only, All Records Lease base
management actions. Billing access is independent; Invoice access is independent
of both Lease facets.

## Retained facts and projections

Migration 122 adds an empty append-only correction history. It does not fabricate
legacy facts or change grants. Original finalization, Asset identities, placement,
generated Invoice content, settlement and generation identities remain retained.
Each correction records actor, acceptance timestamp, reason, exact submission
intent and before/after term dates, premises identity and address snapshot.
Customer and Number cannot be supplied or reassigned through this action.

A correction must explicitly attest a factual error. A valid agreement followed
by renegotiation requires a new Lease. Wrong-Customer repair uses archive of an
eligible original and creation of a replacement.

Start corrections replay initial membership at the new start and every subsequent
coverage action. Fixed end must remain ordered and original initial due must not
follow commencement. Recorded ordinary-notice boundaries must remain eligible;
repair/withdraw notice explicitly before changing an incompatible fixed end.
Changing terms does not remove termination. Factual termination correction is a
separate revision of notice facts; withdrawing a mistaken termination restores
coverage only if the complete timeline and master eligibility validate.

The shared authority/master fence serializes correction, coverage, finalization,
placement and archive writers. Projection guards retain inclusive Lease coverage
boundaries, non-overlap across all finalized Leases, one-Asset minimum and
cross-Customer placement validation in both directions. Premises changes select
active same-Customer Locations; they never move equipment. Existing Lease detail
continues to show unplaced/wrong-premises exceptions.

## Billing and recovery

Source-change flags mark generated Invoices for authorized manual review without
editing them. Ungenerated monthly periods use corrected commencement and begin
in the following calendar month. Already materialized periods that are no longer
owed retain their identity and a disposition; managed cutoff and void suppression
remain in force. Correcting base facts does not grant pricing or Invoice access.

Same-intent retries reconcile the original operation identity. Changed intent or
stale revisions reject. Forms retain authorized proposals after validation or a
known rollback. Stale proposals require explicit compare/revise or discard;
revision refresh does not save. Uncertain outcomes offer exact-submission retry.
Recovery reloads current visibility and management authority before reflecting
the retained proposal. No automatic replay is performed.

Focused qualification evidence and the parent-owned full-suite command are kept
outside source under `.agent-state/v1-spec-18/ticket-70`. This document is an
implementation description, not independent acceptance evidence.

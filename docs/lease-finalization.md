# Lease commencement and initial billing

The initial finalization command is `POST /leases/{id}/finalize`, browser-only,
with `revision`, `billing_revision`, and an explicit ISO `managed_billing_start`.
It requires current Lease View/Manage and Billing View/Manage. New dated
coverage selections require independent Asset View; native commencement retains
the saved Draft selections without expanding selection authority.
Invoice Create, Customer View and Pricing View are not additional grants
for this bounded consequence of approved Lease terms. Subsequent Invoice
operations retain their independent permissions.

The command runs in `WithinActor` under the shared master/authority fence. It
validates active dependencies, ordered dates, complete selected-Asset amounts and
tax, initial due no later than commencement, open-ended coverage non-overlap,
and the complete recorded placement timeline. Fixed-term end does not end
coverage: monthly continuation is part of the same agreement.

## Durable facts

- `lease_finalizations`: one immutable commencement, actor/time, reviewed
  revisions, original dates, premises snapshot, raw terms, explicit cutoff and
  monthly anchor. Retrying the same reviewed revisions/cutoff is a no-op after
  checking current authority; a different intent conflicts.
- `lease_billing_obligations`: immutable initial/monthly identities and approved
  commercial inputs with Lease/period/Asset source lines. Initial allocations
  are grouped together; each month is a separate obligation. Pre-cutoff charges
  are excluded. The first not-yet-eligible month is retained, together with the
  immutable monthly anchor for subsequent scheduling.
- `lease_invoice_generations`: an immutable, unique obligation-to-Invoice link.
  It remains after ordinary Invoice edits or voiding. Replay cannot generate a
  replacement. Deferred constraints require Invoice and obligation provenance to
  commit together; the Invoice's Lease and Customer cannot be changed.

Eligible obligations (due within 30 calendar days or already due) use the
ordinary Invoice number/status/financial/audit writer in the finalization
transaction. Invoice date is the current company date. Generated documents stay
Draft; no delivery or settlement is performed. Any failure, including audit or
deferred checks, rolls back the whole operation and its number allocation.

Raw tax terms must be complete and valid before Customer exemption applies.
Exact rational comparison admits numerically equal spellings and rejects
different effective nonzero rates. Zero-rate Assets become non-taxable lines;
Customer exemption removes otherwise-valid rate conflicts. The existing exact
commercial calculator owns totals and rounding. No rate blending or tax-based
Invoice splitting occurs.

## Owning boundaries

This slice supplies the transactional HTTP command, browser review/receipt,
date-derived current reads and finalized base coverage contract. Draft writers
cannot change finalized terms, dates or selections, including moving a selection
out to another Draft. Generated and pending obligation snapshots both remain
immutable. Pending does not mean permission to mutate approved inputs: a later
reasoned revision must explicitly reconcile pending obligations and flag generated
documents for review, never rewrite or automatically replace a generated Invoice.
Those revision/termination/manual-review commands belong to their later owning
tickets. Retained Terminated facts are readable here without adding a workflow.

The ongoing scheduler/retry worker belongs to #67 and must recheck current
eligibility and tax facts without silently repricing snapshots. Downstream related
views and Job context are separately owned. These boundaries are not missing #66
features. Full race/browser qualification and independent review remain separate
from focused source verification.

## Browser review and recovery

`GET /leases/{id}/finalize` requires the same joint Lease and Billing grants as
the command. It reads both revisions under one authority fence, shows selected
terms and placement warnings, and requires an explicit managed billing start.
It is a review, not a promise of eligibility: the POST rechecks current facts.
Successful POST redirects to this page's durable finalization receipt, including
the cutoff and generated/pending obligation counts. It does not expose Invoice
links or grant Invoice authority. The ordinary shared save interaction handles
transport uncertainty; users can reopen this page to check committed state after
a lost response. Exact replay remains a currently authorized no-op.

## Read-derived renewal calendar

The base Lease domain read includes `LeaseCalendar`: lifecycle plus the current
monthly renewal's inclusive `RenewalStartsOn` and `RenewalEndsOn`. Renewal dates
are empty during Draft, Scheduled, Active and Terminated states. The company-local
civil date comes from `lease_dependency_today`; reading never updates agreement
state or generates billing. List (both facets), Billing and history headers use
the same company-date derivation; historical audit payloads remain captured facts,
not retroactively recalculated events. Field API access remains independently denied.

`DraftLeaseBase.Coverage` exposes the retained finalized Asset intervals with
inclusive starts/ends (nil end means ongoing), distinct from `AssetIDs` Draft
selection intent. The base DTO and base history contain no commercial terms.
Billing retains its own DTO/grants and exposes lifecycle without base coverage,
premises, collaboration or renewal-date fields. Reading coverage needs Lease View,
not independent Asset View; new dependency selections still need their own All
Records View, while saved selections do not expand selection authority. All saved
dependencies are revalidated for company, ownership, archive and timeline eligibility
at finalization. No Invoice Create prerequisite is imposed.

The first renewal starts the day after the inclusive fixed end. Every later
boundary is calculated from that original anchor, clamping to the target month's
last day. A January 31, 2024 anchor therefore produces February 29 and March 31
boundaries (not March 29). The January renewal ends February 28; the February
renewal ends March 30. This agreement calendar is distinct from rent due on the
first day of each month after commencement.

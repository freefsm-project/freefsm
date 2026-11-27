# Lease CSV exports

From `/leases/report`, choose **Review operational CSV export** or **Review
Billing CSV export**. GET `/leases/report/export` reviews the full selection;
POST accepts its operation UUID and opens the existing durable export status
page. Both are authenticated browser routes with production CSRF protection.
There is no field API report, export, receipt or download route.

## Selection and authority

Both contracts require `lease.export` and `lease.view` All Records. Billing
additionally requires `lease.billing.view` All Records. A privileged reader can
deliberately choose operational mode: it does not query or disclose Billing
facts, columns or financial predicates. Clear a Billing predicate before choosing
operational mode; the application never silently changes the selection.

The filters are `as_of`, `search`, `lifecycle`, `customer`, `asset`, `premises`,
`ending_from`, `ending_through`, `archive`, `placement`, `billing`, and `sort`.
Empty archive selects active records; `all` and `archived` require archived
Lease authority. Inclusive ending bounds use termination when present, otherwise
fixed-term end. Related selections require independent current View and archived
access, even when no Lease matches. Billing predicates require compatible
Invoice/Pricing authority; negative generation predicates additionally require
All Records Invoice/Pricing and archived-Invoice access.

Export includes **every matching Lease**, not the browser page. Report links
drop `page`/`per_page`; the export endpoint rejects them as options. Sorting is
the existing numeric document Number and ID ordering, with both reversed for
`number_desc`; display labels are never parsed or re-sorted. There is no separate
operational queue option: start, fixed end, first monthly transition, termination,
coverage and placement are explicit operational facts.

Review preserves all selected filters, shows the exact fixed columns and creates
no job. Confirming queues work; generation captures a single authorization-fenced
snapshot. Blank `as_of` resolves to the company date **at generation**, once.
Every row identifies that effective date, company timezone and snapshot instant.
Explicit dates cannot be future dates. The selected position uses corrected
effective history, not everything an operator knew at that recording time.

Lease coverage is distinct from physical Asset Placement. Fixed end is inclusive;
the first Month-to-Month transition is the next day, not the monthly rent due
date. Termination is inclusive except the owning scheduled-cancellation semantics.
Unknown historical agreement facts are not reconstructed from today's lifecycle.

## Billing meaning and unavailable history

- Selected-date rent uses corrected effective terms. Scheduled rent is separate.
  Revisions retain before/after amounts, tax rates, reason and recording/effective
  dates; only revisions recorded by selected company-day end appear, including
  future-effective approved revisions. Actor and Asset identities are independent.
- Generated total, tax and captured lines come from the retained generation's
  obligation revision, never current rent or edited Invoice lines. Invoice ID
  identifies that generation; obligation revision `0` explicitly means the
  original obligation. Manual and effective Invoice status remain separate;
  effective status is `unavailable` without Settlement authority.
- Settlement rows represent ordinary Payment allocations and Credit Applications;
  their `reversed_on` field records reversal dates, rather than emitting separate
  Reversal rows. Adjustments retain their Invoice target, dates, amount and reason.
  These are provenance, not a new Lease ledger or balance.
- Current `pending` includes owed ungenerated periods through today +30 civil
  days, including `failed`; `failed` is the unresolved-failure subset.
  `ungenerated` selects owed absence due by the selected date. These populations
  overlap and must not be added. Scheduled initial obligations and retained owed
  obligations on terminated nonarchived agreements remain subject to owning
  entitlement. Retained owed periods beyond the window are `not_yet_eligible`.
  Pending does not certify the writer's additional eligibility checks.
- Archived agreements have no actionable pending queue. Retained owed absence
  within the generation window is `inactive` for the current date and
  `ungenerated` for a historical date. Outside that window it is
  `not_yet_eligible`, before archive handling applies. Not-owed disposition keeps
  its owning reason. Voiding an Invoice does not make its consumed period pending
  again.
- Past retry/failure history was not retained. Historical output has
  `failure_history=unavailable`; retained owed generation absence within the window is
  `period_state=ungenerated`, never pending, failed or “no failure.” Historical
  **pending and failed filters are rejected**, with retained inputs for correction.
- Without complete Invoice/Pricing/archived-Invoice authority, a period placeholder
  has both `period_state=unavailable` and `history_status=unavailable`;
  inaccessible generations are never inferred as absent.
  Independently visible positive Invoice facts can still appear.
- Missing historical Draft/imported termination details produce explicit
  unavailable position fields. Predicates whose membership depends on missing
  facts fail actionably rather than silently omit candidates. Dependent Billing
  totals are `unavailable`, not zero. Retained independent provenance may remain.

## Fixed CSV contracts

The ordered headers below are immutable for these versioned kinds. Each CSV has
typed detail rows followed by exactly one `summary`, including for zero matches.
Snapshot metadata appears on every row. Common Lease identity is repeated on
detail rows only. No File links, attachment identities or unrestricted JSON are
included.

Money is exact signed two-decimal text, without a currency symbol; rates retain
decimal text. Applicable booleans are `true`/`false`. Dates are ISO company dates;
instants are RFC3339Nano. IDs are decimal, except settlement UUIDs. A blank means
inapplicable or independently unauthorized, **not zero**. Known zero money is
`0.00`; unavailable dependent amounts/status are literal `unavailable`.
`history_status` describes the availability of the projected facts. It also marks
the period placeholder `unavailable` when incomplete generation authority prevents
disclosing whether a generation exists; that case does not assert missing history.
CSV quoting preserves Unicode/newlines; formula-leading cells have the engine's
reversible leading apostrophe.

### `lease-operational-A-v1` — 27 columns

```text
row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,starts_on,fixed_ends_on,termination_on,first_monthly_on,renewal_starts_on,renewal_ends_on,asset_id,asset_number,asset_name,coverage_starts_on,coverage_ends_on,placement,matching_lease_count,history_status
```

| Row type | Meaning |
| --- | --- |
| `lease` | Authorized identity and agreement dates through renewal dates; retained position availability. |
| `coverage` | Lease context plus independently visible Asset identity, coverage interval and placement. |
| `summary` | Snapshot metadata and distinct `matching_lease_count`; no financial total. |

Coverage children use Asset Number/ID order. Placement distinguishes `unknown`,
`unplaced`, `wrong_premises` and placed states. Unknown association history has
`history_status=unavailable`; it is not “unplaced.”

### `lease-billing-A-v1` — 59 columns

```text
row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,asset_id,effective_on,recorded_at,actor_id,reason,monthly_rent,scheduled_monthly_rent,tax_rate,revision_id,before_rent,after_rent,before_tax_rate,after_tax_rate,period_kind,due_on,period_state,obligation_id,obligation_revision_id,invoice_id,invoice_number,generated_at,generated_total,generated_tax,tax_exempt,manual_invoice_status,effective_invoice_status,review_required,line_ordinal,line_amount,line_taxable,line_billing_revision_id,settlement_id,settlement_kind,settlement_amount,settlement_effective_on,reversed_on,adjustment_id,adjustment_kind,adjustment_amount,adjustment_starts_on,adjustment_ends_on,matching_lease_count,matching_monthly_rent,failure_history,revision_line_ordinal,history_status
```

| Row type | Fields beyond common snapshot/authorized Lease identity |
| --- | --- |
| `lease` | Monthly/scheduled rent, failure history, position availability. |
| `rent` | Independently visible Asset ID, selected-date `effective_on`, rent or scheduled rent and tax rate. This date is not an invented revision date. |
| `billing_revision` | One retained change: revision ID + retained `revision_line_ordinal`, before/after rent and tax rates, effective/recorded dates, reason and visible Asset/actor. |
| `period` | Kind, due date, state, retained obligation ID when available and reason; no inferred Invoice balance. |
| `invoice` | Captured generation total/tax/rate/exemption, generation instant, Invoice identity, obligation/revision, manual/effective statuses and review flag. |
| `invoice_asset` | Captured line ordinal, amount, taxable flag, Billing revision, visible Asset ID and Invoice/obligation references; generated totals are not repeated. |
| `settlement` | Invoice reference, settlement ID/kind/amount/effective/reversal dates; no repeated Invoice amount. |
| `adjustment` | Adjustment ID/kind/amount/start/end, Invoice target, visible Asset/actor, recording instant and reason. Invoice number may be blank. |
| `summary` | Metadata, distinct matching Lease count, selected-date in-force `matching_monthly_rent` and history availability. |

Within each Lease: lease row; rent by source Asset ID; revisions by effective
date/ID and retained change position; periods by due/kind; Invoices by due/ID,
each followed by captured lines then settlements in effective-date/ID order;
adjustments by ID. Private Asset ordering never creates a new exported ordinal.
Revision key is revision ID + retained line ordinal; captured Invoice line key
is Invoice ID + captured line ordinal.

**Only `summary.matching_monthly_rent` is the additive report rent total.** Lease
and rent rows represent different granularities; adding them double-counts.
Child rows never increase the matching Lease count. Scheduled/future rent,
Invoice totals, settlements and adjustments are never summed into this total.

## Durable status, download and recovery

The shared export engine publishes bytes, authorization references, generation
audit and Ready atomically. Pending/Running work survives worker restart. Files
are requester-only for 24 hours from Ready, subject to integrity checks and
current whole-artifact authorization. Losing any requisite row, original/current
scope anchor, archived or sensitive authority blocks the entire file; bytes are
never silently filtered. Gaining authority does not enrich prepared bytes.

Prepared current-date pending/failed snapshots remain valid across midnight
within that lifetime: download rechecks authority, not new selection membership
or today's date. Domain edits do not rewrite immutable bytes. Authorized recovery
opens a fresh filter-preserving review and operation ID. A now-historical
pending/failed selection may require the user to revise its date; recovery never
silently changes it. Denied recovery exposes no stored filters or IDs.

See [Lease position reports](lease-position-report.md) for browser preferences,
pagination and report detail semantics. Ordinary report browsing needs no Export
permission. Tests and database/browser qualification are maintained separately;
this document is a contract, not an assertion of acceptance results.

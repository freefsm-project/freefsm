# Lease position reports

`GET /leases/report` is an authenticated browser-only report. The Lease workspace
links to it. `authorization.Service.LeaseReport` returns the authorized report
rows and matching totals for later export integration; it does not create exports.

## Date and financial meaning

- An empty date means the current company date. A selected date is inclusive and
  cannot be in the future. Effective agreement/coverage facts include accepted
  factual corrections; this is not a reconstruction of what an operator knew at
  a past recording time.
- Lifecycle and renewals reuse the owning civil-date calendar. Coverage uses the
  corrected inclusive domain timeline; placement uses association periods, whose
  end is exclusive. Coverage does not imply physical placement.
- Effective monthly rent uses original approved terms, membership changes and
  effective billing revisions. It is not earned revenue or a Lease balance.
  Scheduled rent is shown separately and is not included in in-force rent totals.
- Generated amounts use the immutable obligation revision actually captured by
  each generation. Later Invoice edits or Lease changes cannot reprice this
  column. Invoice workflow transitions and generation/review/adjustment recording
  dates are bounded by the selected company day's end.
- Payment allocations, explicit Payment-funded Credit Applications and their
  effective Reversals remain separate provenance entries. No unapplied Credit is
  netted and no separate Lease receivables ledger is produced.
- Due managed schedule periods without a generation are explicitly ungenerated;
  termination dispositions and recorded voids are distinct. Manual abatements,
  fees and damages retain their target Invoice, dates, amount and reason.
- Drafts have no backdated effective agreement. A later-created Draft is omitted
  from an earlier date; historical Draft details come from retained detail-change
  snapshots. Missing retained Draft details are explicitly unavailable.

## Authority and consistency

Lease base View gates the report. Billing View independently gates rent and
financial projections. Related Customer, Location and Asset labels/filters use
their current independent View/archived grants. Invoice provenance additionally
requires current Invoice and Pricing access, and settlement entries require
Settlement View. Archived Lease access is independent. Financial exception
filters require compatible Invoice/Pricing and All Records Lease Billing grants
before evaluation. Hidden related records contribute no report links or labels.

One authority-fenced repeatable-read transaction supplies filters, matching
totals, page rows and embedded details. Authorization is acquired before the
snapshot and retained through commit. This is read-only product behavior: no
new permission grants, migrations, commercial writes or browsing audit events.

Number ordering has an identity tie-breaker, including descending order; page
sizes are 25/50/100. Refresh and page links retain the selected filters. Every
successful response states Updated at. Query failures return HTTP 503 with a
filter-preserving retry link, not an empty report. Ordinary record links open
their separately authorized current workspaces; embedded report details are the
selected-date projection.

## Verification

Production PostgreSQL/HTTP tests are in `lease_report_test.go` and
`lease_report_history_test.go`; the persisted responsive journey is
`testdata/browser/lease-report.cjs`. The ticket's durable qualification runner
records exact source/log hashes, prior required test roots and explicit database
URLs. Full `make test` qualification and independent acceptance remain coordinator
gates after the focused milestone.

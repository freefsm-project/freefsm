# Lease manual adjustments

From a finalized Lease's **Billing → Manual abatements, fees and damages**, select
an eligible Invoice, review its current commercial values, and enter an Asset,
explicit positive USD-cent amount, ordered relevant dates, and reason. Abatements
use outage dates and become negative Invoice lines; fees and damages become
positive lines. Taxability is explicit and uses the selected Invoice's single
rate, Customer exemption, document adjustments, and ordinary commercial calculator.
The entered amount is the line amount before those document adjustments and tax.
Neither dates nor terms automatically award value, impose charges, or judge default.

## Authority and transaction boundary

The browser-only action requires Lease Billing View/Manage and independent Invoice
View/Edit and captured Pricing View for the selected Invoice. Assigned-Job Invoice
authority still requires its current active assignment. Base Lease access and the
field API expose no adjustment amounts. Application history additionally filters
by current Invoice/pricing authority before returning its before/after values.

One shared-authority transaction checks current session, grants, Lease and Customer
archive state, Asset membership/availability, selected Invoice Customer and editable
state, and the reviewed Invoice revision. It appends the explicit line, recalculates
valid nonnegative pretax/final totals, updates the ordinary settlement projection,
and writes immutable application history and both Invoice and Lease audit. A failed
validation, concurrent settlement/archive, stale revision or failed audit commits
none of these effects. There is no parallel Lease settlement ledger.

An application binds its operation identity to actor, Lease and complete intent.
An exact retry returns the existing outcome; changed intent rejects. Validation
retains the proposal. Stale state requires comparison and explicit revise with a
new identity, or discard; an uncertain transaction exposes exact retry only.

## Settled reductions and provenance

Active settlement locks an Invoice. A fully paid reduction must instead be applied
to an eligible unpaid or future Invoice for the same immutable Lease Customer.
If none exists, create the future Invoice through the ordinary independently
authorized Invoice workflow, then select it here. There is no noncash Credit,
fabricated Payment, or reversal of a valid receipt.

Migration 124 adds empty immutable application history without backfill or new
grants. Each application retains Lease, Asset, Invoice, kind, exact amount, relevant
dates, reason, actor/time and before/after commercial values. A selected ordinary
Invoice retains Customer provenance without becoming a generated obligation.
Applications never reset generation windows, regenerate voided periods, alter
approved generation snapshots, or change coverage/placement. Existing optional
Add charges remain distinct from `manual_abatement`, `manual_fee`, `manual_damages`
line provenance and monthly/initial generation identities.

## Owning verification

`TestLeaseAdjustmentApplicationHTTP`, `TestLeaseAdjustmentSettledReductionAndBoundariesHTTP`,
`TestLeaseAdjustmentOrdinaryInvoiceCustomerHTTP`, `TestLeaseAdjustmentSettlementCommitOrders`,
`TestLeaseAdjustmentPopulatedUpgrade`, and `TestLeaseAdjustmentBrowser` exercise
authenticated HTTP/PostgreSQL, denial, exact values, replay, atomic failure,
commit ordering, upgrade preservation and responsive browser recovery. Browser
tests require `BROWSER_TESTS=1`, Chromium and repo-local Playwright. These tests do
not replace complete qualification or independent acceptance.

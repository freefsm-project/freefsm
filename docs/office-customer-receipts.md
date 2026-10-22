# Office Customer receipts

From a Customer dashboard, **Customer receipts** opens the independently
authorized office action and settlement history. A receipt records money actually
received, with a fixed positive amount and Customer. The optional Invoice selector
contains only currently visible, active finalized Invoices with positive due.
Submission rechecks those conditions and Customer ownership inside the existing
authority transaction. It allocates exactly `min(receipt, current due)` to that one
Invoice; the remainder creates one source Credit. No Invoice means the entire
receipt is Unapplied Payment Credit. Neither Credit nor an Unapplied Payment nets
against receivables or changes another Invoice's settlement state.

## Authority and reconciliation

`payment.credit` (All Records), its Customer financial prerequisites, and current
Customer access authorize entry. Invoice selection independently requires Invoice
View. Neither bounded Payment, Credit View, settlement-history View nor reversal
authority follows from receipt entry. Receipt history requires `settlement.view`;
available Credit retains its separate `credit.view` projection. Unapplied reversal
requires `payment.reverse` and settlement View. Active applications/refunds block
reversal until their effects are unwound through the established actions.

The office form retains only an opaque operation UUID in session storage. A lost
response locks submission until the original receipt is checked. Missing history,
including after restore, is uncertainty rather than permission to submit again.
Reviewing a different receipt requires explicit reconciliation and reloads current
authority. Session/action denial purges input. The existing bounded web/API forms
and their fixed-amount semantics are unchanged; no field API Customer receipt or
Credit route exists, regardless of grants.

Receipt history reconciles the full amount to its initial allocation and origin
Credit, showing stable Payment and Credit identities, recorded method label and
identity, effective date, reference, note, actor, entry time and channel. Origin
Credit is not another cash receipt. Unknown historic actor/time/label stays unknown
or displays the recorded stable method; today's configuration is not backfilled.

## Storage and installed data

Migration `092_customer_payment_receipts` only permits a null `invoice_id` on the
installed `invoice_payments` table. The physical table name is retained to preserve
installed identifiers, foreign keys, restore compatibility and readers. Customer
ownership, immutable originals and allocation/source constraints remain. A null
Invoice cannot acquire an Invoice allocation through the composite foreign key.

The migration does not regenerate Credit, replay legacy cutover or infer cash
receipts. Existing wholly unapplied legacy receipts may retain their recorded
Invoice link and zero allocation; that known provenance is not rewritten to null.
Existing source identities, reversals, method metadata and grants remain intact.
Whole-instance backup includes the same financial tables and operation admissions.
Fresh authentication after restore permits receipt reconciliation; post-backup
facts cannot be reconstructed from a missing receipt.

## Focused verification

`TestOfficePayment*` exercises real PostgreSQL production HTTP routes, exact split
and Unapplied accounting, independent grants, immutable originals, audit rollback,
concurrency/current-authority loss, explicit dependency unwind, unchanged
receivables/aging, installed upgrade and cross-owner whole-instance backup/restore.
The persisted Chromium journey covers both forms of receipt, lost responses,
history and session denial. It requires `BROWSER_TESTS=1`, Playwright and Chromium;
backup requires `FREEFSM_BACKUP_TEST_ADMIN_URL`. Always set a disposable
`FREEFSM_TEST_DATABASE_URL`. A skipped opt-in is not evidence of passing.

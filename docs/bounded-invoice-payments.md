# Bounded Invoice Payments

The Invoice **Record Payment** action accepts one fixed positive USD amount
against its original active finalized non-Draft/non-Void Invoice. The entire
amount must fit the current positive due. It never shrinks, splits, retargets or
creates Customer Credit. Customer and Invoice eligibility, current assignment,
session and independent `payment.record` permission are checked in the committing
transaction. Assigned Jobs and All Records are supported; Customer-only Invoices
require All Records. Financial, Credit and settlement-history viewing remain
independent. The action context exposes only the selected Invoice's due.

## One settlement writer and stable method identities

Bounded and established office settlement reuse the immutable Payment,
allocation, activity and settlement-projection writer. They share the existing
authority fence before row locks, including commercial edits, workflow, archive
and Credit writers. Accepted payment, allocation, projection, immutable audit,
common operation admission and safe operation receipt commit together. Audit or
storage failure rolls back admission as well as money. Manual workflow is retained.

The existing method identities are `cash`, `check`, `credit_card`, `transfer` and
`other`, displayed initially as Cash, Check, Card, Bank Transfer and Other.
**Company settings → Payment method labels** uses the existing independent company
configuration View/Manage grants. Overrides customize labels only; method meaning
and identity cannot change. New receipts snapshot their accepted display label,
Other description and channel. Other requires a description regardless of rename.
No payment-credential fields are collected or stored.

Effective dates use company-local calendar dates, current or historical, never
future. The immutable receipt retains Customer, Invoice, amount, date, method,
reference, note, actor and entry time. Correcting financial history continues to
use the established reversal model; renaming configuration never rewrites it.

## Web and field API reconciliation

The website remains online-only. Its Payment form retains only the pending
operation UUID in session storage and locks further submission while the outcome
is unknown. **Check original Payment receipt** reconciles after a lost response or
reload. A missing receipt is uncertainty, not permission to repeat a Payment.
After restore, explicitly reconcile recorded history before proposing another
intent. Terminal rejection requires explicit review and a new operation identity.

Field API routes:

- `GET /api/v1/invoices/{id}/payment-methods`
- `POST /api/v1/invoices/{id}/payments`
- `GET /api/v1/payment-operations/{operation}`

The POST requires `operation_id`, original `job_id`, exact two-decimal string
`amount`, stable `method` and ISO calendar `date`; optional `reference`, `note`,
`other_description` and nonfuture `occurred_at` are bound into intent. The field
API has no Customer-only fallback or Credit action. Unknown fields reject.
The local OpenAPI document describes commands, errors and safe receipts.

Operation IDs share the existing deployment-wide admission registry. Unchanged
same-client replay returns the original receipt; changed intent or another
mutation family rejects. Receipt lookup exposes only outcome, Payment UUID,
operation UUID and acceptance time, including after Invoice access loss. Fresh
authentication of the original user permits read-only reconciliation, including
after restore; it does not rebind the original client or authorize replay. Other
users cannot inspect that receipt. Authentication revocation rules still apply.

`internal/api/v1/testdata/payment-reference-harness.cjs` is an executable in-memory
reference reconciler exercised against the real API. It freezes original intent,
blocks replacement while unknown, queries the original receipt and keeps
post-restore missing receipts uncertain until explicit review. It is not a
production encrypted offline cache or an implementation of downstream field sync.

## Installed records and recovery

Migrations `087_bounded_payment_operations` and `088_payment_method_labels` add
new command receipts and label overrides without changing installed financial
records, identifiers, allocations, workflow, idempotency or Role grants. Missing
historical label/description/channel stays null; readers show the recorded method
identity rather than today's label. Absent new request metadata is omitted from
legacy idempotency fingerprints. The reviewed legacy settlement/workflow cutover
continues to report ambiguous source values and never manufactures Payments to
explain an old status.

Whole-instance backup preserves these tables alongside existing settlement and
projections. Restored sessions are revoked. Pre-backup receipts remain queryable
after fresh authentication; post-backup history cannot be reconstructed. The real
cross-root, distinct nonprivileged-owner restore test demonstrates both cases.

## Verification

`TestBoundedPayment*` exercises production web/API routes, persisted Chromium,
real reference-client requests, exact-due/overpayment/invalid-date rejection,
assignment loss under contention, concurrent fixed Payments, audit rollback,
cross-family operation reuse, method history, installed receipt replay and actual
backup/restore. Browser tests require `BROWSER_TESTS=1`, Playwright and Chromium.
Database tests require an explicitly disposable `FREEFSM_TEST_DATABASE_URL`;
restore additionally requires `FREEFSM_BACKUP_TEST_ADMIN_URL`. Run `make lint`
and uncached `make test` with those database variables. Skipped opt-in tests do
not count as passing evidence.

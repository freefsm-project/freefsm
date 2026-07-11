---
status: accepted
---

# Use immutable relational records for invoice settlement

Invoice settlement will use normalized relational, immutable financial records with amounts stored as integer cents in the application's existing implicit dollar currency. This preserves an auditable history, makes settlement constraints enforceable, and avoids the ambiguity and update risks of mutable JSON settlement data.

Each settlement operation will atomically write its financial records, update the Invoice's Settlement State projection, create the existing activity entry, and record idempotency within one database transaction. Operations will use pessimistic locking for the affected Invoice, Customer, and selected credit source so concurrent payments, applications, refunds, and reversals cannot overspend value or produce an inconsistent projection. Original financial records are never edited or deleted; corrections are represented by immutable Reversals.

Invoice workflow statuses remain separate from monetary settlement. Monetary statuses will be removed from the workflow status model, and Settlement State will be limited to Unpaid, Partially Paid, and Paid.

## Considered Options

- Keep settlement details in mutable JSON. Rejected because relational constraints, concurrency control, querying, and an immutable audit history would be weaker and harder to reason about.
- Introduce a generic double-entry ledger. Rejected because invoice settlement does not currently require a general accounting abstraction, and that abstraction would add concepts and operational complexity beyond the domain need.
- Introduce a generic command bus for settlement operations. Rejected because direct transactional application services provide the required atomicity and idempotency without an additional dispatch abstraction.
- Use optimistic concurrency alone. Rejected because invoice, customer, and credit-source contention must prevent concurrent consumption before settlement values are committed.
- Run a gradual dual-read or dual-write migration from JSON. Rejected because parallel representations would create reconciliation and source-of-truth ambiguity.

## Consequences

- A maintenance-window migration will perform a strict cutover from the existing JSON representation to the normalized records; there will be no compatibility period or dual source of truth.
- Every monetary write must participate in the same transaction as its projection, activity, and idempotency updates.
- Contending settlement operations may wait on locks, and lock acquisition must follow a consistent order to avoid deadlocks.
- Credit applications explicitly select and lock one credit source. Refunds consume aggregate available customer credit by locking and allocating the oldest sources first in deterministic FIFO order.
- Integer cents avoid floating-point rounding, but the model remains single-currency until currency is made explicit by a future decision.
- Settlement State is a projection of immutable financial history and may be rebuilt or checked against that history.

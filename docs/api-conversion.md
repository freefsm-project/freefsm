# Reviewed conversion and exact-cycle reversion

The website remains online-only. These API operations use the existing conversion,
commercial receipt, and settlement services.

## Discover and review

Authorized document reads and commercial snapshots expose advisory
`actions.convert` and `actions.revert`. Convert requires Estimate Convert and
Invoice Create. Entering Invoice custom-field values additionally requires Invoice
Edit. Revert requires Invoice Revert and an active cycle. These flags are not
commit authorization or a guarantee that validation and financial prerequisites
are satisfied. Current authority is checked again on submission.

`GET /estimates/{id}/conversion-preview` returns the reviewed commercial revision,
current Invoice date/due defaults, source fields, prospective Invoice fields, and
definitions. `POST` to the same path previews explicit `custom_fields` without
creating a document, cycle, number, or receipt. Missing-required validation may
accompany an incomplete preview. Pricing is omitted without independent View.
The local OpenAPI defines the closed `ConversionPreviewEnvelope` contract.

Pairing uses explicit conversion keys, equal types/precision, and identical
ordered option labels. Names do not establish a pair. Actual paired source values
win over entered values; unpaired Invoice defaults apply. Missing paired values
do not silently inherit a target default. New conversion validates current
required Invoice fields.

## Submit once

Send `operation_id`, `job_id`, and the reviewed `revision` to
`POST /estimates/{id}/convert`; include `custom_fields` only when deliberately
entering Invoice values. Any active Estimate Status is eligible. The result is a
new numbered Draft Invoice for the same Customer and Job, with no finalize,
Delivery, or settlement effect. All-record authority can retain the existing
archived Job link; Assigned Jobs authority cannot use that exception.

The receipt returns the target identity and exact `cycle_id`. The Estimate becomes
hidden. Current Comments, Files, and Tags move with their stable authors/uploaders;
PDF versions and Delivery evidence retain their original identities. Pending
original-parent operations never automatically follow the moved collaboration.

After a lost response query `/commercial-operations/{operation_id}` or retry the
unchanged original intent with the same ID. A terminal repair requires a newly
reviewed ID. Neither a new cycle nor a restore authorizes automatic replay.

## Revert exactly the reviewed cycle

Read the current Invoice and capture both `revision` and `cycle_id`. Restore an
archived Invoice first. Explicitly unwind all active settlement and origin
Payment-funded Credit dependencies through existing financial services, including
dependent applications/refunds and their required reversals. Revert never performs
that unwind automatically, shrinks an amount, or invents a financial event.

Submit `operation_id`, `job_id`, `revision`, and `cycle_id` to
`POST /invoices/{id}/revert-to-estimate`. Content drift and cycle drift reject;
an old request cannot revert a later cycle. Success restores the original Estimate
as Draft with current Invoice shared content and original Estimate-only values.
Invoice-only content remains in a hidden tombstone. Re-conversion creates another
cycle and a new Invoice Number; prior cycles and receipts remain unchanged.

The cycle's captured pairing governs recovery despite later configuration changes.
Unsafe representation rejects recovery. Permitted retired/newly required recovery
exceptions defer validation to the next ordinary save without filling defaults.

## Synchronization and recovery

Active Invoice reads/snapshots expose the exact current `cycle_id`. Ordinary reads
cannot retrieve either hidden document. Losing a previously synchronized original
parent returns `409 resnapshot_required` with `purge: all`; purge and obtain a fresh
authorized snapshot rather than retaining tombstone content. A restored Estimate
keeps its stable synchronization identity. Neither snapshot replacement nor an
operation receipt grants access to hidden snapshots or revives dependent intent.

## Ordered batches and protected staging

`POST /sync/operations` supports `estimate.convert` and `invoice.revert` alongside
the existing commercial operations. Each member carries `operation_id`, `type`,
the original numeric document ID as a string `target`, `depends_on`, and a closed
`commercial` payload. The payload contains `job_id`, `revision`, optional
`occurred_at`, and either conversion `custom_fields` or reversion `cycle_id`.
The operation ID belongs only in the outer member. Omitted domain preconditions
produce terminal receipts; malformed/null/unknown properties reject the envelope
before any member executes.

Each member commits independently through the existing commercial receipt and
conversion transaction. Only earlier or known-success dependencies are eligible.
Failed or unknown predecessors block descendants; unrelated members continue.
Changing transport, target, ordered dependencies, cycle, revision, occurrence, or
entered values under an existing ID is rejected. Retry a transient operation with
its original ID only after reconciliation; repair a terminal operation and its
descendants explicitly with reviewed new IDs.

The protected `CommercialClient.stageBatch` supports `convert` and `revert`,
encrypting the complete reviewed intent before transport. `submitBatch` durably
marks the group unknown before network I/O. Reopen the store and reconcile each
original receipt after interruption or a lost response; never infer that an
unreceived result failed. `repairBatch` requires explicit dependencies and retains
the original target and exact reviewed cycle. It never rewrites descendants.
Whole-instance restore keeps pending intent locked until fresh authentication,
synchronization, and receipt reconciliation; repeated restore invalidates that
reconciliation again. Neither restore nor authentication resubmits a batch.
# Safe conversion history

`GET /api/v1/estimates/{id}/conversion-history` and
`GET /api/v1/invoices/{id}/conversion-history` resolve lineage through the
currently visible, active Job-linked parent. Parent View and independent
Provenance View are required; `actions.provenance` is advisory discovery.
Customer-only documents and archived Jobs are outside this field projection.

History uses the existing authorized activity projection, not raw Timeline
metadata or immutable source snapshots. Hidden-parent conversion events expose
only retained event/document identity, actor label and acceptance time, with
empty facts and `provenance_estimate_id`; they confer no ordinary document access.
Visible-parent events retain independent facet filtering. Missing occurrence
means unknown or not projected, never acceptance-time inference.

Pages are descending acceptance time/ID, default 50, maximum 200 (`limit`).
Supported filters are `actor`, `action`, `from` (inclusive), `until` (exclusive)
and opaque `cursor`. Follow `next_cursor` with unchanged filters; authority is
rechecked on every page. This is current-authority history, not a frozen sync
snapshot. Unknown/repeated filters and invalid/filter-mismatched cursors reject.

The protected commercial adapter's `conversionHistory(target, cursor)` stores
one encrypted page with its current parent. Reopen obeys the offline lease;
synchronization replaces the parent projection, and authority/parent loss
withholds or purges history through the existing commercial cache owner.

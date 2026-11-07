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

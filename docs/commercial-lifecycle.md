# Reviewed Estimate and Invoice lifecycle

Estimates start in Draft and remain editable and convertible while active, including Accepted, Rejected and Completed. Acceptance is an explicit manual action. Mail acceptance may advance an unchanged proposal to Sent; it never means customer acceptance.

Invoices start in Draft. Only the independent Finalize action leaves Draft, selecting Invoiced and retaining the reviewed dates and content. Without active settlement, non-Draft invoices may move among Invoiced, Sent and Void, or return to Draft. Void is reversible, excludes receivables, settlement and sending, and new PDF versions contain the literal **VOID** mark even when the workflow label is renamed. Prior PDF bytes remain immutable.

Active allocations or Credit Applications lock the whole commercial document, including Customer/Job anchors, dates, notes, terms, Custom Fields and workflow actions. The shared authority/settlement fence and database commercial guards apply across writers. Independent Comments, Files, Tags, PDF generation and eligible sending remain available. Mail acceptance on a settled Invoice records Delivery evidence without changing its locked manual workflow.

Effective status is a projection: manual Void; unsettled manual Draft; active partial settlement; Paid with an active settlement effect or a non-Draft zero-total Paid projection; otherwise the preserved manual status. Paid and Partially Paid are not manual options. Zero-total Finalize creates no Payment, returning to Draft removes the Paid display, and reversal reveals the retained manual choice without a new workflow event.

## Field API and executable reference

Authorized Job-bound document GETs return `revision`, `manual_category`, `effective_category`, `workflow` choices (stable `id`, `category`, `meaning_revision`, current name/color), and Invoice `settlement_locked`. Pricing remains separately authorized. Choices describe vocabulary, not an authorization grant.

POST `/api/v1/estimates/{id}/status`, `/api/v1/invoices/{id}/status`, or `/api/v1/invoices/{id}/finalize` with:

```json
{
  "operation_id": "a new client-generated UUID",
  "job_id": 12,
  "revision": "the originally reviewed whole-commercial revision",
  "status_id": 34,
  "meaning_revision": "the reviewed target semantic revision"
}
```

Optional `occurred_at` is a nonfuture offset timestamp; server acceptance is recorded independently. Status and Finalize do not require Edit. Cosmetic rename/recolor is safe. Identity replacement or category changes (including ABA) require explicit reselection. Disjoint commercial changes conflict, while Comments/Files do not advance the commercial revision.

The existing commercial operation admission and immutable receipt ledger own these actions. Accepted mutation/activity/audit/receipt commit atomically. GET `/api/v1/commercial-operations/{operation_id}` reconciles an uncertain result, including a nonsensitive accepted effect after access loss. A terminal conflict needs a newly reviewed operation ID; transient audit/storage failures leave no terminal receipt and permit the original identity to retry. Never automatically finalize, unwind settlement, replay unknown financial intent, retarget, or overwrite another transition.

`internal/api/v1/testdata/commercial-reference-harness.cjs` stages frozen lifecycle intent without contacting the server, retains unknown outcomes, and checks the original receipt before any retry. Its persisted conformance test exercises real API requests, lost responses, replay and stale intent. This is the existing in-memory executable reference, not a production encrypted offline cache; the separately owned protected offline client remains unchanged.

## Installed data and downstream boundaries

The reviewed workflow migration and settlement preflight remain authoritative for legacy installations. They require explicit mapping of ambiguous legacy statuses and retain actual recorded payments, identifiers and PDF bytes. This change neither reconstructs old acceptance/transition history nor creates financial facts or new Role grants. Exact commercial arithmetic and historical snapshots remain on the accepted financial contract. Conversion cycles, bounded Payment features, Lease billing and broader Delivery workflows retain their owning modules/tickets.

# Reviewed Estimate and Invoice lifecycle

Estimates start in Draft and remain editable and convertible while active, including Accepted, Rejected and Completed. Acceptance is an explicit manual action. Mail acceptance may advance an unchanged proposal to Sent; it never means customer acceptance.

Invoices start in Draft. Only the independent Finalize action leaves Draft, selecting Invoiced and retaining the reviewed dates and content. Without active settlement, non-Draft invoices may move among Invoiced, Sent and Void, or return to Draft. Void is reversible, excludes receivables, settlement and sending, and new PDF versions contain the literal **VOID** mark even when the workflow label is renamed. Prior PDF bytes remain immutable.

Active allocations or Credit Applications lock the whole commercial document, including Customer/Job anchors, dates, notes, terms, Custom Fields and workflow actions. The shared authority/settlement fence and database commercial guards apply across writers. Independent Comments, Files, Tags, PDF generation and eligible sending remain available. Mail acceptance on a settled Invoice records Delivery evidence without changing its locked manual workflow.

Effective status is a projection: manual Void; unsettled manual Draft; active partial settlement; Paid with an active settlement effect or a non-Draft zero-total Paid projection; otherwise the preserved manual status. Paid and Partially Paid are not manual options. Zero-total Finalize creates no Payment, returning to Draft removes the Paid display, and reversal reveals the retained manual choice without a new workflow event.

## Field API and executable reference

Authorized Job-bound document GETs return `revision`, `manual_category`, `effective_category`, `workflow` choices (stable `id`, `category`, `meaning_revision`, current name/color), and Invoice `settlement_locked`. Pricing remains separately authorized. Choices describe vocabulary, not an authorization grant.

### Commercial snapshots and deltas

GET `/api/v1/sync/estimates` and `/api/v1/sync/invoices` use independent document
View grants; neither Job View nor Pricing View is a prerequisite. `scope=assigned`
is the default; `scope=all` requires the corresponding All Records document View.
Both surfaces contain active documents linked to active Jobs, excluding Customer-only
documents, archives and conversion-hidden tombstones. Customer and Job IDs are
commercial anchors and grant no master/Job context.

Each record carries a stable opaque `id`, the existing command endpoint identity
`legacy_id`, immutable anchors, number, title, notes, terms, dates, Custom Field
values, and the originally reviewed whole-commercial `revision`. Manual `status_id`
and `effective_status_id` accompany the categories and current workflow vocabulary.
The `actions` booleans independently describe `edit`, `status` and `finalize` under
current grants and lifecycle/settlement eligibility. They are advisory; submission
rechecks current authority, fields, totals and workflow through the existing services.
The workflow list is vocabulary, not a list of currently eligible transitions:
Draft Invoice leaves Draft only through Finalize to Invoiced. Derived statuses are
excluded from selectable vocabulary. Non-Invoice `finalize` is always false.

Pricing View alone controls `line_items`, `tax_rate`, `financial`,
`arithmetic_version` and exact-string `totals`; losing it never leaves those fields
in a continued frozen page. Totals and public line inputs use the same services as
direct document GETs, evaluated from the captured state rather than newer rows.

The shared synchronization protocol defaults to 50 records (maximum 200) with
`page_size`, `page`, `cursor` and `scope` only. Follow `next_page` using unchanged
scope/page size, then retain `completion_cursor`. Pages share one frozen watermark;
the next delta catches changes committed while those pages were delivered. A
cursor has one durable successor, so replay does not silently advance its baseline.
Continuity is retained for at least 30 days. Record IDs are never reused.

Grant, assignment, pricing-facet, anchor or document loss returns a non-disclosing
403 purge or 409 `resnapshot_required`/`purge_and_resnapshot`, invalidating existing
client baselines including earlier pages and successors. Purge before requesting a
fresh snapshot; no inaccessible document fields or removal explanation are returned.
Restore of a grant does not revive invalidated cursors. A 503 is retryable without
advancing the cursor. An empty successful delta means no changed visible records,
not a failed fetch. Commercial sync does not expand Job creation-input projection.

Before repairing pending edits after refresh, query the original receipt and compare
only currently authorized state. Keep the original revision until the user explicitly
reviews a replacement intent with a new operation ID, or discards it. These server
projections do not themselves supply an encrypted reference-client implementation.

### Protected commercial reference

`internal/api/v1/reference/commercial-client.cjs` exports `CommercialClient` and
the existing `ProtectedStore`. Use a separate protected directory per document
kind. Initialize, authenticate and explicitly `synchronize('estimate'|'invoice')`;
`synchronize(kind, true)` advances a completed delta. `view()` requires a live
offline lease and a completed synchronization.

`stageEdit(opaqueID, reviewedRevision, changedValues)` captures the original
revision, Job and Customer. `stageLifecycle(opaqueID, reviewedRevision, choice,
'status'|'finalize')` captures the selected identity, category and meaning revision.
Staging is offline and copies caller input. `submit(operationID)` is explicit.
On uncertainty, `reconcile(operationID)` queries the original receipt first;
fresh synchronization is then required. `compare(operationID)` refreshes current
authority before exposing the original review, proposed intent and current state.
`repair(operationID, newlyReviewedRevision, valuesOrChoice)` creates a new ID;
`discard(operationID)` removes local intent. Neither refresh nor restart submits
or rewrites work. Restore-only uncertain work additionally requires receipt lookup
and explicit `acknowledgeUncertain` before replacement.

The shared protected owner encrypts persisted state, locks on expiry and failed
authority writes, purges ordinary revocation, and separates restored-epoch review.
Failed refresh/comparison and receipt contact withhold persisted observations;
successful commercial synchronization cannot unlock inherited private observations
from another domain. The production HTTP/PostgreSQL regression
`TestCommercialProtectedReferenceAPI` exercises encrypted restart, disjoint edit
conflict, explicit repair, lost-response/scope-loss receipts, revocation, restore,
workflow/zero-total behavior and failed comparison. Ordered commercial batch
dispatch and the complete settlement/field/workflow-change reference matrix still
require further implementation and conformance evidence.

Retained documents without a recorded manual status remain readable: `status_id`, `manual_category` and an unknown `effective_category` are explicitly `null`. Their current revision, workflow choices and Invoice settlement lock remain available for deliberate review. A read never supplies Draft, creates a transition or invents financial history. Repair requires an explicit reviewed Status selection; its event preserves the unknown prior status rather than claiming a historical Draft or acceptance.

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

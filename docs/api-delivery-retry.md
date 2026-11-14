# Field API Delivery retry

`POST /api/v1/delivery-retries` accepts `operation_id` (canonical UUID),
`document_type` (`estimate` or `invoice`), positive original `document_id`,
`job_id`, `delivery_id`, and nonblank `reason`. Optional `occurred_at` is a
nonfuture RFC3339 instant. No replacement message, PDF, recipient, or File is
accepted. No query parameters are supported.

Failed/Bounced retry queues the same immutable communication and message ID;
automatic attempt counters and their original time window are retained. Accepted
is a no-op. Cancelled is terminal. Intentional resend uses new reviewed creation
intent, not retry. Every newly admitted retry rechecks independent document View,
Delivery Request, PDF Download, and File Download (for extras), lifecycle and
original Job binding under the authorization commit fence.

200 returns `{ "data": { "operation_id": "…", "outcome": "accepted",
"delivery_ids": [123] } }`. This acknowledges the command, not mail handoff.
403/404/409 domain rejection retains a terminal receipt (`forbidden`, `not_found`,
`source_drift`, `cancelled`, or `invalid_state`). Invalid wire input is 400;
identity/type/client/intent misuse is 409. Infrastructure/audit failure is 503 and
rolls back admission, effect and receipt together.

Always reconcile `GET /api/v1/delivery-operations/{operation_id}` after response
loss. The original authenticated user/client can retrieve disposition and stable
effect identity after scope loss, without message, recipients or attachments.
An authoritative 404 permits explicit unchanged retry with the same identity;
terminal repair requires a new reviewed identity. Another client cannot read the
receipt or reuse its UUID. Same-intent replay never sends again.

## Batch boundary

API-05 requires bounded ordered per-operation atomic batches. The current implementation,
as with PDF generation and Delivery creation, uses a dedicated domain-owned retry
admission transaction.
The generic batch currently dispatches authorization/File/commercial operations;
Retry is not dispatched by the generic batch. OFF-11 staging in this reference
reconciles the dedicated endpoint. Retry has no
dependent byte transfer or PDF generation: it refers only to an existing frozen
Delivery. Clients must not submit `delivery.retry` to `/sync/operations`.
This is an evidenced implementation boundary, not proof that the complete API-05
batch requirement is satisfied. Independent Spec review must assess whether retry
must also participate in ordered batches and dependency lookup; the generic
dependency lookup currently recognizes creation receipts, but not retry receipts.

## Protected reference handoff

Persist the complete retry intent in the existing encrypted store under the
original deployment/user/client. Use finite lease and original-scope rules for
staging, receipt-first reconciliation and explicit same-intent retry only after
authoritative absence. On scope loss retain only permitted reconciliation identity;
never silently replace document/Job/Delivery IDs. Handle all terminal receipt
outcomes, including creation's `invalid_dependency` and `dependency_failed`.
`DeliveryClient` in `internal/api/v1/reference/delivery-client.cjs` implements this
using `CommercialClient` and `ProtectedStore`. `reviewDelivery` records the exact
message review; `stageDelivery` and `stageRetry` only persist pending intent.
`submit` performs an explicit online command. After uncertainty, `reconcile`
queries the receipt before any target review, and `retryUnreceived({confirm:true})`
rechecks absence before submitting the unchanged original identity. Terminal repair
uses `repairDelivery` or `repairRetry` with new IDs; retry repair keeps the original
Delivery target. These accepted receipts acknowledge commands, not SMTP acceptance.

Use one document kind per store. The executable protected reference requires the
platform Secret Service key backend and a private, real directory; it is not a
browser offline feature. Ordinary authority loss purges restricted intent; restore
requires fresh authentication/synchronization, receipt lookup and explicit uncertain
outcome acknowledgement before new-ID repair. Full qualification and independent
acceptance remain separate gates.

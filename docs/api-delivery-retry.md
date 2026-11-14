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

API-05 requires bounded ordered per-operation atomic batches; it does not require
every domain endpoint to execute inside that dispatcher. As with PDF generation
and Delivery creation, retry uses its dedicated domain-owned admission transaction.
The generic batch currently dispatches authorization/File/commercial operations;
putting retry there would invert Delivery ownership or introduce a second admission
transaction. OFF-11 staging reconciles this dedicated endpoint. Retry has no
dependent byte transfer or PDF generation: it refers only to an existing frozen
Delivery. Clients must not submit `delivery.retry` to `/operations/batch`.

## Protected reference handoff

Persist the complete retry intent in the existing encrypted store under the
original deployment/user/client. Use finite lease and original-scope rules for
staging, receipt-first reconciliation and explicit same-intent retry only after
authoritative absence. On scope loss retain only permitted reconciliation identity;
never silently replace document/Job/Delivery IDs. Handle all terminal receipt
outcomes, including creation's `invalid_dependency` and `dependency_failed`.
Protected reference implementation and full qualification are the next job.

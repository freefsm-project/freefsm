# Ordered offline operation batches

`POST /api/v1/sync/operations` accepts an `operations` array in submitted order.
The API and encrypted native reference own this protocol; the website is online-only.
The supported operations are `job.field_notes`, `job.status`, `job.subtask`, and
`job.fields`; see [Status/Subtask synchronization](api-job-field-sync.md) and
[Custom Field Values synchronization](api-job-custom-field-sync.md).
Operation-specific synchronization for clocks, Comments, Files, documents and Service Entries remains
with its owning tickets.

```json
{"operations":[{"operation_id":"80000000-0000-4000-8000-000000000001","type":"job.field_notes","target":"42","depends_on":[],"notes_revision":"original-opaque-revision","field_notes":"Reviewed original text","occurred_at":"2026-10-15T12:00:00Z"}]}
```

## Envelope, ordering and dependencies

The limits are **100 operations** and **1 MiB (1048576 bytes) decoded UTF-8 JSON**,
including whitespace. Exact limits pass size validation. An excess rejects413
before any admission/effect; invalid envelope shape also rejects before dispatch.
Use `application/json`, without content encoding (or `identity`). Compressed
requests are unsupported415. File bytes use separate transfer endpoints.
Properties are exact-case, unknown/duplicate properties and duplicate operation
IDs reject. An empty array is valid. No query parameters are accepted.

Every operation commits independently through its existing owning domain service,
common admission registry, current-authority transaction and immutable receipt.
Earlier acceptance survives later failure. No occurrence-based sorting, global
batch transaction, target mapping, revision substitution or implicit retry occurs.

`depends_on` holds original operation UUIDs. References must name earlier batch
members or known successful receipts in the same deployment/user/client binding.
Forward/self/cyclic-leading edges and unknown/cross-binding references return
`invalid_dependency`. Descendants of unsuccessful earlier operations return
`failed_dependency`; unrelated work continues. Admission alone is not success.
Checks consult owning receipts inside the committing transaction. A terminal own
receipt is recovered before dependencies are reconsidered, so retry cannot
rehabilitate or alter a previously rejected intent.

## Receipts, loss, retry and repair

HTTP200 contains `data.results` in submitted order, each with `operation_id` and
a safe `receipt` and/or typed `error`. Receipt fields are the existing operation
ID, code, acceptance time and optional effect identity; no target text/comparison
is disclosed. Notes receipts are individually queryable at
`GET /api/v1/job-execution-operations/{operation}` after target access loss and
fresh same-user authentication. Fresh authentication does not rebind replay or
dependency authority. Each new effect rechecks current authority/lifecycle.

Original target spelling, type, dependencies, text, revision and occurrence are
immutable intent. Changing any under the same UUID returns
`operation_intent_changed`, leaving the original receipt intact. A batch command
cannot be replayed through the single-Notes endpoint to bypass its dependencies.
Receipts/admissions have no expiry (exceeding30 days); lifetime effects and all
existing identities/history remain intact. No migration/backfill/grant change.

Network loss may leave a committed prefix. Reconcile each original receipt before
explicitly retrying unresolved work with identical IDs/intent. Audit/storage
rollback leaves no receipt/admission/effect. An uncertain Commit remains unknown.
Transient results retain the ID and a retry delay; they are not terminal failures.
The authenticated request budget and12 sync requests/minute/client apply to whole
batch requests. HTTP429 has typed timing and `Retry-After`; service errors use
five seconds unless a more specific delay is supplied. Never parse messages.

Rejected operations and blocked descendants require separate explicit reviewed
repair: new IDs, current reviewed revisions/text and reviewed dependency UUIDs.
Repairing a parent never rewrites descendants. Accepted operations cannot be
repaired. No target/revision/amount rewrite is performed to make a batch succeed.

Status and Subtask intent shapes and semantic recovery are documented in
[Job field synchronization](api-job-field-sync.md). Batch receipts normalize the
owning effect identity as `effect_id`; a Subtask's individual receipt remains at
`/subtask-operations/{operation}` with its existing `subtask_id` field. Both refer
to the same immutable receipt/effect. Original operation family determines lookup.

## Encrypted executable

Use the [existing protected Notes executable](api-offline-notes.md):

- `stage-batch-notes`: `job`, `text`, explicit `depends_on` array.
- `submit-batch`: ordered `operation_ids` array; all must be pending batch intents.
- `reconcile`: individual original `operation_id`; never submits.
- `repair`: original `operation_id`, reviewed `text`, `notes_revision`, and explicit
  reviewed `depends_on` for a batch operation. Repair retains its original target.

All selected entries are durably unknown before transport. Actual Secret Service
keys and AES-GCM protect pending data across fresh processes. Ordinary authority
loss purges inaccessible intent, including dependency/target bindings; failed
authority-loss persistence keeps the existing quarantine marker. Restore is
distinct: pending work remains locked until fresh authentication, full current
sync and explicit outcome reconciliation. A restored receipt miss is uncertainty,
never permission to replay. General Offline Authorization is not Clock staging.

Local OpenAPI3.1 describes request/response/error schemas. Production HTTP with
migrated PostgreSQL, encrypted reference/restart/interruption, dependency,
concurrency and rollback tests exercise this seam. Existing online Notes/browser,
installed-history and protected-key regressions remain required gates.

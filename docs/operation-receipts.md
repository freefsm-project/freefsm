# Operation receipts and Field Notes recovery

The API uses the [shared operation admission registry](operation-admission.md).
Domain services own their receipts and commit admission, effects, attribution,
history, activity and immutable audit in the same transaction. This contract does
not replace pairing's one-use redemption or session/lease lifecycle protection.

## Identity and retention

Generate a nonzero UUID for each reviewed mutation. Keep its deployment, original
user/client, operation family, target, revision, exact content and occurrence
unchanged for a retry. Changing any intent requires a new reviewed UUID. A UUID
cannot be recycled for another family, even after a terminal rejection. The
authenticated session identifies an API client; changing a device label does not
change that binding.

Current receipt and admission stores have **no expiry**. They are immutable and
retained beyond the 30-day minimum; effect identities remain for their lifetime,
including after subsequent edits and across entry points. No cleanup job removes
them. Restart is not an identity boundary. Backups include these database facts.

Whole-instance restore replaces history rather than merging it. **A receipt
missing after restore does not prove that the original effect never happened.**
Receipts cannot reconstruct post-backup effects. Restore revokes sessions; fresh
authentication and current-authority synchronization precede explicit review of
uncertain intent. Never automatically repeat it. Inaccessible cached material
must still be purged. The in-memory reference locks pending work when receiving
`instance_restored` / `locked_review`; it is not durable encrypted storage or a
complete restore/offline client.

## Field Notes protocol

1. Read `GET /api/v1/jobs/{id}/field-notes` under current Job View.
2. Explicitly review text and send `POST` with `operation_id`, `notes_revision`,
   `field_notes`, and optional `occurred_at`. Empty text clears the facet.
3. A success returns `{data:{operation_id,code:"accepted",effect_id,accepted_at}}`.
   An identical-text save with the current revision is a no-op: it retains the
   original effect identity, author and history. Stale/ABA revisions conflict
   even when text happens to match. Status, Subtasks and Custom Field Values have
   independent revisions.
4. After response loss, query
   `GET /api/v1/job-execution-operations/{operation_id}` before explicitly retrying.
   A found receipt is authoritative for that operation. Replaying the unchanged
   operation returns the same disposition, never another revision/activity row.
5. A terminal rejection is also queryable. For `stale_notes_revision`, read
   currently authorized Notes/history, compare, and explicitly resubmit reviewed
   text using a **new** ID and revision, or discard. No forced overwrite, merge,
   target substitution or original-intent rewriting is available.

The receipt contains only operation ID, code, acceptance time and (when present)
stable effect ID. It contains no Job ID/title, Notes text, historical comparison
or author details. The original user can read it after assignment/Job access loss
or fresh authentication. Fresh authentication does not rebind the original client
or authorize that new client to replay the old command. Sensitive reads always
check current authority; an invisible Job/history returns 404 and an independently
denied action on a visible Job returns 403.

## Errors and recovery

Use machine fields, never parse messages.
`error.status` carries the numeric HTTP status alongside `code`, `message`,
`request_id`, safe `details`, and `recovery_disposition`. Retryable failures also
carry `retry_after_seconds` matching the `Retry-After` header. A dependency without
more specific timing uses five seconds. Terminal domain failures include
`stale_notes_revision`, `legacy_review_required`, `invalid_intent`,
`invalid_occurrence`, `forbidden` and `not_found`. Changed-intent reuse returns
`operation_intent_changed` and leaves the original receipt untouched. Malformed
transport or an unrecoverable operation ID does not manufacture a terminal
receipt. Authentication failure never supplies replay authority.

Transport loss has an unknown outcome. HTTP 429 and dependency/service failures
are nonterminal: retain the original ID and exact intent, respect retry timing,
and reconcile unknown outcomes. Audit/activity/storage failures roll back the
entire effect and admission. No success may be inferred from a failed request or
an absent receipt. Receipt lookup itself can fail transiently; wait and retry the
lookup rather than resending a mutation.

The executable `/api/v1/job-execution.js` reference distinguishes pending,
unknown, accepted, terminal and locked-review intent. `reconcile` never sends a
mutation. Only a confirmed ordinary receipt miss unlocks an explicit unchanged
retry after transport loss; a restore lock is not unlocked by a miss. `repair`
requires a terminal operation and a new ID; `discard` removes local intent.
Logout purges it. Hosts supply authenticated transport and current lease policy;
the website remains online-only.

## Executable contract evidence

`TestNotesReferenceOpenAPIHTTP` serves the production router and the actual local
OpenAPI 3.1 document. JSON Schema 2020-12 validation checks requests/responses,
formats and documented status codes while the executable Node reference exercises
success, unavailable retry, changed intent, conflict/new-ID repair, history,
authentication, and a committed socket loss followed by assignment removal. The retained
browser test covers online persisted Notes editing and comparison. Focused
concurrency, audit/activity rollback, current-authority races and fresh-process
receipt replay tests exercise the same migrated PostgreSQL service.

The retention fixture injects a synthetic acceptance clock older than 30 days;
it does not claim historical production execution. Installed-history fixtures
remain separate and preserve source facts and nonprotected grants. New API
fields are additive; clients ignore unknown response fields and safely review
unknown codes instead of treating them as success.

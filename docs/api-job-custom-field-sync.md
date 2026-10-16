# Offline Job Custom Field Values

The API and shared encrypted reference support `job.fields` patches. The website
remains online-only. Job View and independent `job.fields.edit` at Assigned Jobs
or All Records are authoritative; planning Edit never implies value-edit authority.
Status/Subtask synchronization is documented separately in
[api-job-field-sync.md](api-job-field-sync.md).

## Snapshot, revisions and definition evolution

Each authorized `/api/v1/sync/jobs` record includes a complete `custom_fields`
facet: definitions, values, independent per-value revisions, `can_edit` and recorded
history. Definitions include active/retained retired fields, types, precision,
requiredness, help and stable option identities. Existing numeric definition IDs
and exact option-text identities remain unchanged; never infer IDs from labels or
positions. Job targets use existing opaque sync IDs; legacy decimal IDs still work.

Definitions, values and history share the coherent snapshot and delta protocol.
Treat records as complete replacements and honor scope-loss purge/resnapshot.
Cosmetic valid changes preserve value tokens; changed values, including ABA, rotate
only their own tokens. Current type/options/precision/requiredness/retirement,
authority and archive state are checked in the committing transaction.

Representations remain: single-line `text`, multiline `textarea`, exact decimal
string `number` (configured0–18 fractional places), ISO local `date`, explicit
`"true"`/`"false"` `checkbox`, exact-key `select`, and distinct-key string-array
`multiselect`. Required checkboxes never infer No from omission. Offline editing
defers unrelated missing required values, not changed required values. Creation
and conversion retain their own rules, including existing minimal Job creation;
the offline exemption never extends those rules. Defaults never backfill existing
Jobs through editing, sync, restore or reactivation.

Retired fields remain readable with history, leave ordinary entry and reject edits.
Retired options reject new selections but permit unchanged retained selections.
Reactivation resumes validation without filling values or retrying rejected work.

New validated value audits explicitly mark `field_value_format: typed-v1`.
Unmarked/unrecognized historical before/after data remains exact read-only
`history[].legacy_values` evidence, not guessed modern definition mappings.
Current typed events use `before`/`after` arrays. Both retain recorded actor/time;
sealed Role/session data and fabricated historical revisions/authors are excluded.
Existing immutable audit is never rewritten or backfilled with the new marker.

## Atomic patch, receipt and explicit repair

Use guarded `PATCH /api/v1/jobs/{id}/custom-fields` or an ordered batch member:

```json
{"operations":[{"operation_id":"82000000-0000-4000-8000-000000000001","type":"job.fields","target":"original-job-sync-identity","depends_on":[],"revisions":{"1":"original-opaque-value-token"},"values":[{"definition_id":1,"value":"12.50"}]}]}
```

Every changed value, audit/history and receipt commits atomically. Only submitted
values' revisions are checked; unchanged fields and unrelated Job facets coexist.
One stale changed value rejects the whole patch. No force, text merge, retargeting
or default replay is available. Shared100-operation/1MiB limits and dependencies
follow [the batch contract](api-operation-batches.md). Exact nested names, duplicate
members, declared value shapes and nonnull string revisions are preflighted before
admission; a malformed later member cannot commit earlier work. Canonical but
domain-invalid values retain typed per-operation failure and unrelated continuation.

The common registry binds original target/type/dependencies/values/revisions and
occurrence. Same-ID replay returns the original receipt; transient rollback retains
the ID. Terminal repair requires authorized comparison/discard and a new ID.
Batch intent cannot bypass dependencies through direct PATCH. Historical nonbatch
receipts cannot acquire new batch binding; their original direct replay remains.

`invalid_custom_field` remains stable. Optional receipt/error-details `reason` is
`invalid_field_shape`, `invalid_field_identity`, `invalid_field_type`, `invalid_text`,
`invalid_number`, `invalid_date`, `invalid_checkbox`, `invalid_option`,
`required_field`, `retired_field` or `retired_option`. Never parse message text;
unknown future reasons require safe review.

`GET /api/v1/job-field-operations/{operation}` returns original-user safe disposition,
recorded acceptance time and optional reason after scope loss/fresh authentication.
Accepted receipts retain recorded `job_id`; `effect_id` is the existing operation
UUID only when immutable mutation audit records that effect. Historical/no-op rows
without evidence never acquire fabricated effects; hidden rejected targets expose
no resolved identity. Ambiguous historical client rows return409
`operation_identity_conflict`, never an arbitrarily chosen receipt. Original
client/target-specific lookup remains available. Fresh auth permits lookup, never
rebinding replay. Restore receipt absence is uncertainty, not failure.

## Shared encrypted executable

Use `internal/api/v1/reference/offline-notes.cjs` and the existing
[protected-store setup](api-offline-notes.md). After authentication and complete sync:

| Action | Explicit inputs |
|---|---|
| `stage-fields` | `job`, changed `values`, reviewed `depends_on` |
| `submit-batch` | ordered `operation_ids`, including other supported field families |
| `reconcile` | original `operation_id` |
| `repair-fields` | rejected ID, reviewed `values`, current `revisions`, reviewed dependencies |
| `discard` | original `operation_id` |

Staging captures changed revisions and is pending, not accepted. Refresh/synchronize
for comparison before repair. Restart/auth/sync/reactivation never submits. Unknown
outcomes reconcile first; accepted work cannot be repaired as rejection; descendants
need separate new IDs. Notes, Status, Subtasks and values share one protected key,
AES-GCM envelope, lease, serialization and durable quarantine. Ordinary scope loss
purges inaccessible content; safe receipt-family metadata survives. Restore locks
recoverable intent until fresh auth, complete current sync and explicit reconciliation.

Migration087 adds only nullable validation_reason, preserving every prior receipt
field and leaving historical reasons NULL. No grant/identity/value/history rewrite.
Local OpenAPI3.1 and executable production-HTTP/real-PostgreSQL/protected-reference
checks cover the contract; scoped Chromium/Secret Service is not device certification.

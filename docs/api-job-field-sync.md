# Offline Job Custom Field Values

The API and encrypted reference support `job.fields` patches. The website stays
online-only. Planning Edit does not grant `job.fields.edit`; current Job View and
the independent Assigned Jobs or All Records value-edit grant are authoritative.

## Snapshot and definition evolution

Each authorized `/api/v1/sync/jobs` record includes a complete `custom_fields`
facet: definitions, current values, per-value opaque revisions, `can_edit`, and
recorded before/after history. Definitions include active and retained retired
fields, type, precision, requiredness, help, and stable option identities. Existing
numeric definition identities and exact option text identities remain unchanged;
never infer identity from the display name or array position. Job targets use the
existing opaque sync identity (the legacy decimal identity is still accepted).

Definition, value and history changes participate in the same coherent snapshot
and subsequent deltas. History exposes only recorded field facts and attribution,
not sealed Role/session evidence. No past revision or author is synthesized.
Treat records as complete replacements and follow the existing sync purge rules.

Cosmetic valid changes do not invalidate value tokens. A token advances only for
its changed value, including ABA. Current type, precision, options, requiredness,
retirement, authority and archive state are validated in the committing domain
transaction, not against a cached definition revision.

The seven value representations remain: single-line `text`, multiline `textarea`,
exact decimal-string `number`, ISO local `date`, explicit `"true"`/`"false"`
`checkbox`, exact-key `select`, and distinct-key string-array `multiselect`.
Required checkboxes never infer No from omission. Offline patches defer unrelated
missing required fields, but not required changed values. Creation and conversion
retain their own validation rules (including the existing minimal Job creation
rule); this patch exemption never applies to those commands. Defaults are never
backfilled by synchronization, editing, restoration or reactivation.

Retired fields remain readable with history but are excluded from ordinary entry.
Submitted retired fields reject; a newly selected retired option rejects while
an unchanged retained selection remains valid. Reactivation resumes current
validation without filling missing values or retrying rejected operations.

## Atomic patch and immutable intent

Submit a single guarded PATCH at `/api/v1/jobs/{id}/custom-fields`, or an ordered
batch member at `/api/v1/sync/operations`:

```json
{"operations":[{"operation_id":"82000000-0000-4000-8000-000000000001","type":"job.fields","target":"original-job-sync-identity","depends_on":[],"revisions":{"1":"original-opaque-value-token"},"values":[{"definition_id":1,"value":"12.50"}]}]}
```

Each member commits every changed value and its history/audit/receipt atomically.
Only submitted values' revisions are checked. Unchanged fields and unrelated Job
facets may change concurrently. A stale changed value rejects the entire patch.
There is no force overwrite, text merge, target rewrite or default replay.

Batch limits, ordering and dependencies follow [the shared contract](api-operation-batches.md).
The original target spelling, type, dependencies, values, revisions and occurrence
are bound in the existing common operation registry. A batch intent cannot bypass
dependencies through the single-PATCH endpoint. Same-ID replay returns the original
receipt; transient failure retains the ID. Audit failure rolls back the complete
effect and admission. A terminal rejection needs explicit comparison/discard before
a new-ID repair, including after reactivation or regained authority.

The existing `invalid_custom_field` outcome is retained. New receipts add optional
`reason`: `invalid_field_shape`, `invalid_field_identity`, `invalid_field_type`,
`invalid_text`, `invalid_number`, `invalid_date`, `invalid_checkbox`,
`invalid_option`, `required_field`, `retired_field`, or `retired_option`.
Single-PATCH conflicts also include the reason in `error.details`. Branch on typed
codes, never message text; unknown future reasons require safe review.

`GET /api/v1/job-field-operations/{operation}` returns the original user's safe
receipt (operation ID, outcome code, recorded acceptance time, optional reason)
after scope loss or fresh authentication. It returns no field values/comparison.
Fresh authentication permits lookup, never rebinding a command to a new client.
The existing target-specific receipt lookup remains compatible. After response
loss, query the original receipt before retry; restore receipt absence is uncertainty.

## Encrypted reference

Use the existing `internal/api/v1/reference/offline-notes.cjs` executable and
[protected-store setup](api-offline-notes.md). Authenticate and synchronize, then:

| Action | Explicit inputs |
|---|---|
| `stage-fields` | `job`, changed `values`, reviewed `depends_on` array |
| `submit-batch` | ordered `operation_ids` |
| `reconcile` | original `operation_id` |
| `repair-fields` | rejected `operation_id`, reviewed `values`, current `revisions`, reviewed `depends_on` |
| `discard` | `operation_id` |

Stage captures only changed revisions from the authorized snapshot. Synchronize
again for comparison before repair. No staging, restart, sync or reactivation
submits work automatically. Accepted work cannot be repaired as a rejection.
Unknown work must reconcile first; failed descendants require their own new IDs.

These commands use the same platform-protected key, AES-GCM envelope, lease,
writer serialization and durable quarantine as Notes. Ordinary scope loss purges
all inaccessible field intent/context; only safe receipt metadata survives.
Restore instead locks recoverable intent for fresh authentication, complete sync
and explicit outcome reconciliation. No second encryption or authority mechanism
is introduced. This is a reference harness, not mobile-device certification.

Migration087 adds a nullable reason to existing immutable receipts without changing
identities, grants, stored values or history, and without guessing historical reasons.
Local OpenAPI3.1 documents the additive request/response contract.

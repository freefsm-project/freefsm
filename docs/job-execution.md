# Job Status and Field Notes

`job.status` and `job.field_notes.edit` are independent canonical grants at
Assigned Jobs (`J`) or All Records (`A`) scope, each with compatible Job View.
Neither grants planning, linking, scheduling, assignment, pricing or Custom
Field editing. Current Field Notes and their activity history follow Job View.
Completed/Canceled are still actionable; archive is read-only. Reopening still
requires repairing archived operational Contact/Location/Asset dependencies.

## Shared web/API commands

The Job page contains a Status form and links to the independent Field Notes
editor and revision history. Job activity links to the same authorized history.
Planning forms no longer edit Field Notes. Each form has its own operation ID
and facet revision; its receipt commits atomically with the change, immutable
history and protected audit. A lost browser response offers original-ID receipt
lookup or an explicit unchanged retry. It never silently generates a new ID.
Status retains its existing operational activity event in that same transaction.
Access-loss responses purge stale form defaults, comparisons and Job context;
the nonsensitive original receipt identity remains available for reconciliation.

The paired bearer API provides:

* `GET /api/v1/jobs/{id}/status-intents`: current Status choices and semantic
  intent tokens, observed under one authority fence.
* `POST /api/v1/jobs/{id}/status`: `{operation_id, status_id, status_revision,
  meaning_revision, occurred_at?}`.
* `GET /api/v1/jobs/{id}/field-notes`: current text, notes revision and edit/review
  disposition.
* `POST /api/v1/jobs/{id}/field-notes`: `{operation_id, notes_revision,
  field_notes, occurred_at?}`. An explicit empty string clears text.
* `GET /api/v1/jobs/{id}/field-notes/history`: immutable authorized history.
* `GET /api/v1/job-execution-operations/{operation_id}`: requester's immutable
  terminal receipt, without resource content or comparisons.

Operation IDs are client-generated nonzero UUIDs. Receipts are retained without
an expiry (thus at least 30 days); effects retain their UUIDs for their lifetime.
Intent binds Job, facet, revisions, exact text/target and optional occurrence to
the authenticated user/client session within this deployment. Same-intent replay
returns the original receipt; changed intent or client rejects. Receipt lookup
also works after fresh authentication by the original user, without reviving the
old session or permitting that client to replay the old intent.

Status uses the existing independent opaque record and semantic revisions.
Cosmetic rename/recolor/order changes do not change meaning. The same still-valid
target is a no-op preserving effect identity and attribution. Different stale
targets and ABA reject; category changes (including away-and-back) or replacement
identities require explicit reselection. Notes reject every stale revision,
including identical text after ABA. A current-revision identical-text save is a
no-op. Neither facet conflicts with unrelated planning or the other facet.

Terminal conflicts use `status_reselection_required`, `stale_notes_revision`,
`legacy_review_required`, `job_dependency_conflict`, `not_found`, or `forbidden`.
Review current authorized state and submit a **new** explicitly reviewed ID to
repair; restore/reassignment never reopens a terminal receipt. Storage/audit
failures return a retryable error with no partial effect or terminal receipt.
Reconcile uncertain commits before retrying the **same** ID. After scope/archive
loss comparisons and history are withheld, while receipt disposition, effect ID
and acceptance time remain queryable. Invalid sessions must authenticate again.
Restore can remove post-backup receipts; it never proves an uncertain effect
failed or authorizes automatic repetition.

`/api/v1/job-execution.js` is an executable in-memory offline reference module.
It stages immutable reviewed intent without network access, enforces its supplied
authorization lease expiry, reconciles original IDs, requires new IDs for terminal
repair, and purges inaccessible intent. The browser acceptance fixture drives it
against the production HTTP API. A production client must supply platform-secure
storage and current lease/authority synchronization; the website stays online-only.

## Non-destructive upgrade

Migration `076_job_execution` retains `jobs.tech_notes` as the physical current
projection, without renaming/deleting any recorded data. It adds an independent
revision, immutable baseline row, receipts and an independent planning revision.
Contact-membership changes advance planning's token too, preserving existing
stale/ABA protection while notes and Status cease to invalidate planning forms.
Existing Status identity/category/revisions and activity attribution remain
untouched. Earlier workflow cutover migrations already reject ambiguous semantic
mapping non-destructively; this migration does not infer labels or assign grants.

Every pre-migration current note is identified as a migration baseline. Its
historical author and acceptance time remain NULL; `recorded_at` is when the
baseline was observed, not a claimed edit time. Existing recorded activity is
retained. Nonempty unattributed notes are gated until a protected Administrator
reviews the source and explicitly acknowledges the unknown history with a reason
on the Field Notes page. Review neither rewrites text nor invents prior authors,
times or revisions; it is audited and immutable once recorded. No nonprotected
Role receives new grants. Fresh empty Job baselines are labelled initial state,
not migration history. Unsupported legacy note writers fail closed rather than
overwrite the independently guarded facet.

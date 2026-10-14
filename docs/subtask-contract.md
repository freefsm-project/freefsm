# Stable Job checklists

The Job's **View checklist and history** link supports maintenance, completion,
reopening and immutable history on phones and desktops. Maintenance requires
`job.subtask.manage` at All Records; completion and reopening each require their
own Assigned Jobs or All Records grant. Neither requires general Job Edit.
Completed and Canceled Jobs remain actionable; archived Jobs are read-only.

## Identity, concurrent intent and attribution

Each Subtask has a UUID, nonblank title, explicit order and an independent opaque
completion revision. Renaming and reordering do not change that revision. Same-state
completion/reopening preserves the previous actor/time/revision; a stale opposite
state, including ABA, conflicts. Removal is terminal: the active projection hides
the row but retains its identity and history. Batch reorder names every active
identity exactly once; missing, duplicate and foreign identities reject atomically.

`job_subtasks` owns identity and state. `jobs.subtasks` is the active compatibility
projection, written in the same transaction. Details-save maintenance retains IDs,
never writes completion state, and shares the authorized transaction. The old
positional toggle route is removed and positional service calls fail closed.
Authority, session, assignment and lifecycle are fenced through commit. Effects,
history and protected audit commit atomically; an audit failure leaves no effect
or terminal receipt.

Checklist routes use their own operation/receipt protocol rather than the generic
web save journal. A whole-Job form revision would incorrectly invalidate a field
completion after unrelated renaming or reordering. Keep this boundary when
composing other Job facets; the rendered-form regression covers it. Authentication,
CSRF, current Permission checks and the committing authority fence still apply.

## Existing installations

Migration `076_subtask_identity.up.sql` inventories the original JSON unchanged in
`subtask_legacy_reviews`. It does not manufacture prior completion events, actors
or times. The affected checklist remains blocked until an authenticated protected
Administrator inspects its exact snapshot at `/jobs/{id}/subtasks` and explicitly
approves assigning identities by recorded row position. The retained mapping links
each position to its new permanent UUID. Original title, order and state remain;
unknown actor/time remain NULL and display as unknown. The review itself is audited
at its actual acceptance time, separately from completion history.

Unexpected historical identity/reference fields, malformed rows and blank titles
remain blocked non-destructively for evidence-based remediation. Nothing silently
deletes or normalizes them. Role grants are not inferred, copied or auto-granted.

## API and executable reference

- `GET /api/v1/jobs/{id}/subtasks`: currently authorized active rows and history.
- `PATCH /api/v1/jobs/{id}/subtasks/{uuid}` with
  `{"operation_id":"<uuid>","completion_revision":"<opaque uuid>","completed":true}`.
- `GET /api/v1/subtask-operations/{operation_id}`: exact acting user/client receipt.

The field API exposes completion/reopening only. Missing resources are 404; denied
actions on visible Jobs are 403; stale revisions or changed-intent operation reuse
are 409. Same-intent replay returns the original immutable receipt. After assignment
loss, receipt lookup retains disposition, effect UUID and acceptance time without
title, checklist history or restricted comparisons. Database/audit failures are
nonterminal; explicitly retry the same ID. Never translate an old array index.

OpenAPI documents these endpoints. `internal/api/v1/reference/subtasks-reference.cjs`
is the executable bounded staging/reconciliation reference: staged intent sends
nothing, lost responses reconcile the original receipt, and terminal conflicts need
explicit new reviewed intent. Its caller supplies staging authorization; this slice
does not implement the general offline lease, encrypted cache or batch-sync engine.
Those integrate through the same exact-ID command boundary in their owning slices.

## Verification

`internal/handlers/subtasks*_test.go` uses production routing, real migrated
PostgreSQL, durable history/audit observations, authority waiters, concurrent
completion/removal/reordering, non-destructive upgrade review, and a real Node
reference client. `TestSubtaskPersistedBrowser` additionally requires
`BROWSER_TESTS=1`, Playwright and `BROWSER_EXECUTABLE`. It executes persisted
320px maintenance and completion, including a server-committed lost response.
This scoped Chromium journey is not the separate release browser matrix.

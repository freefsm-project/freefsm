# Status and Subtask synchronization

The field API and encrypted native reference support independent Job Status and
Subtask completion/reopening. The website remains online-only. Planning actions
(add, rename, reorder, remove) remain web-only and retain their existing history.

## One authorized snapshot

`GET /api/v1/sync/jobs` adds these fields to each complete Job replacement:

- `status.id` and `status.meaning_revision` (nullable for an unrecorded Status);
  existing name/category presentation remains compatible.
- `status_revision`: the independent opaque Job Status revision.
- `status_definitions`: configured identity, label, color, category, order and
  meaning revision. Definition IDs are strings identifying existing definitions;
  no alternate UUID namespace or label-based identity is introduced.
- `status_attribution`: actor identity, acceptance and optional occurrence for
  the recorded event matching this exact revision, or null when not recorded.
  Configuration replacement and historical baselines never invent attribution.
- `subtasks`: stable IDs, titles/order, state, independent completion revisions,
  and recorded completion/reopening actor/time. Unknown installed attribution
  remains absent. Removed entries leave the active list.
- `subtask_history`: recorded add/rename/reorder/remove/complete/reopen events,
  actor/time and before/after. Removal retains history and never reuses an ID.
- `subtask_review_required` and `field_capabilities` (`status`,
  `subtask_complete`, `subtask_reopen`). Capabilities describe current grants;
  they do not authorize a later submission.

The existing authorization fence and coherent snapshot cover definitions,
revisions and checklist facts together. Deltas catch cosmetic/semantic definition
changes and every checklist event. Unchanged recorded facts produce no delta.
Current Job View controls all these representations. Scope loss follows the
existing purge/resnapshot protocol and withholds comparisons.

## Original reviewed intent

Submit through the existing ordered `POST /api/v1/sync/operations` envelope:

```json
{"operations":[{"operation_id":"81000000-0000-4000-8000-000000000001","type":"job.status","target":"original-opaque-job-id","depends_on":[],"status_id":"17","status_revision":"original-status-revision","meaning_revision":"original-meaning-revision"},{"operation_id":"81000000-0000-4000-8000-000000000002","type":"job.subtask","target":"original-opaque-job-id","depends_on":["81000000-0000-4000-8000-000000000001"],"subtask_id":"81000000-0000-4000-8000-000000000003","completion_revision":"original-completion-revision","completed":true}]}
```

Both optionally include `occurred_at` at microsecond precision. Each shape is
closed: planning fields and fields belonging to another family reject the whole
envelope before earlier effects. Limits remain 100 operations/1 MiB decoded JSON;
null dependencies reject during preflight. Dependencies grant no authority.

Status accepts any configured target on an active Job without a transition graph.
Completed/Canceled Jobs remain actionable; archive closes field actions. Existing
archived operational dependency checks still govern reopening. Subtask completion
never selects Job Status. Unrelated Notes, values, planning and other Subtasks
do not alter these independent revisions.

The same currently valid Status target is an attribution-preserving no-op, even
when its independent revision is old. Other stale state and ABA reject with
`status_reselection_required`. Cosmetic rename/recolor is safe because identity
and meaning remain valid. Replacement, category changes, and category ABA change
meaning: explicitly reselect and create a new reviewed operation ID.

Complete/Reopen use only the exact Subtask and its completion revision. Same-state
requests preserve actor/time/revision. Changed stale state returns
`subtask_conflict`; a removed/foreign Subtask is `not_found`, never resurrected or
matched by title. Existing independent Complete/Reopen grants are rechecked.

Effects, existing immutable history/audit, common operation admission and owning
receipt commit together. Audit/storage rollback is nonterminal and retains the
same operation ID for an explicit retry after receipt lookup. Changed original
type/target/revisions/dependencies/occurrence under one ID rejects. Earlier
accepted batch members survive later unrelated failures. Terminal repair and
blocked descendants each require a separately reviewed new ID; repairing a
parent never rewrites old descendants.

## Protected executable and recovery

The existing `offline-notes.cjs` executable now exposes a shared
`OfflineFieldClient` (`OfflineNotes` remains a compatible exported alias). It uses
the same protected store, offline authority, expiry, receipt recovery and durable
quarantine; no second encryption or authorization mechanism exists.

After `authenticate` and `synchronize`, choose original IDs/revisions from `view`:

| Action | Explicit reviewed command fields |
|---|---|
| `stage-status` | `job`, `status_id`, `status_revision`, `meaning_revision`, `depends_on` |
| `stage-subtask` | `job`, `subtask_id`, `completed`, `completion_revision`, `depends_on` |
| `submit-batch` | ordered pending `operation_ids` |
| `reconcile` | original `operation_id`, never a mutation |
| `synchronize-delta` | completed current cursor required; no implicit submission |
| `repair-status` | old `operation_id`, explicitly selected `status_id`, current reviewed revisions, `depends_on` |
| `repair-subtask` | old `operation_id`, reviewed `completed`, `completion_revision`, `depends_on`; original Subtask retained |

Staging is encrypted pending intent, not acceptance. Unknown outcomes require
original receipt lookup before retry. The family selects the existing receipt
endpoint, including after sensitive target data is purged. Unknown categories
require review, never an inferred transition. Deltas atomically replace authorized
cache/cursor and retain the established authority-loss quarantine on write failure.
An interrupted synchronization locks disclosure until a complete fresh sync.
Receipt reconciliation also withholds sensitive cache until an explicit complete
fresh sync; recover a group of original receipts first, then refresh current scope
once. Original Status/Subtask intents, revisions and retry IDs are not rewritten.

Ordinary authority loss purges sensitive intent. A nondisclosing `not_found`
conservatively purges that Job's cached/pending content before further review.
Restore keeps recoverable intent encrypted and locked until fresh authentication,
complete current-authority sync and explicit original-outcome reconciliation.
A restored receipt miss remains uncertain, never permission to replay. No
background submissions, automatic reselection, force, or title matching exists.

## Compatibility and conformance

No migration, grant expansion, replacement identity or historical backfill is
needed. Existing single-action and Notes-batch serialized intent bindings remain
unchanged. The local served OpenAPI 3.1 adds discriminated field batch shapes and
optional snapshot fields, preserving earlier clients and receipt routes.
`TestJobFieldSync*` exercises production HTTP with migrated PostgreSQL, actual
protected native storage, served-schema validation, deltas, semantic/ABA conflicts,
same-state attribution, removed identities, explicit dependent repair, concurrent
web/API DML and rollback/replay. Existing persisted browser, workflow, checklist,
protected-store and upgrade checks remain required regression evidence.

Changelog: additive v1 Status/Subtask snapshot fields, two ordered operation
families, and protected reference commands. Existing Notes, direct Status and
Subtask request/response shapes and installed identifiers remain supported.

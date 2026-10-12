# Semantic workflow configuration

Workflow View and Workflow Manage are independent unscoped grants (Manage
requires View). Configuration is available at `/settings/statuses/job`,
`project`, `estimate`, and `invoice`. Read-only users see disabled controls.
The category heading for Projects is **New**; existing custom labels are retained.

Configuration and manual status commands use the shared authorization transaction
lock, current identity/session/grants, and immutable audit. Configuration changes
also retain the ordinary activity projection. Failure of either audit insert
rolls back the entire command. No migration adds nonprotected Role grants.

## Identity and replacement

Renaming/recoloring preserves identity and meaning revision. Reordering and
default selection do not rewrite records. Defaults affect future implicit
selection. A cross-category move changes the meaning revision, including an
out-and-back move. Project display progress projects Completed as 100 without
overwriting stored manual progress; leaving Completed reveals that stored value.

Deletion rewrites every live reference, including archived and conversion-hidden
Estimates, to a same-category replacement. It updates the category default
atomically and does not manufacture record transitions. The tombstone exception
permits only that same-category reference rewrite, not content edits. Foreign
keys reject concurrent references rather than silently setting them to NULL.
Delivery `expected_status_id` remains historical intent after deletion; it is
not retargeted to the replacement. Invoice slots remain fixed and ordered.

## Field API status intent

`GET /api/v1/jobs/{id}/status-intents` returns an authorized list in the ordinary
`data` envelope. Each item contains `status` presentation and an `intent`:

```json
{"status_id":123,"status_revision":"opaque record revision","meaning_revision":"opaque meaning revision"}
```

Submit that intent to `POST /api/v1/jobs/{id}/status`. Both revisions are required.
A discovery request requires current Job View at All Records or an active
Assigned Job scope. Legacy role names grant no access. View alone permits
discovery but not mutation: the POST independently requires Job Status. The web
Job Status command uses that same transaction-bound authority. Completed and
Canceled active Jobs can be changed by any appropriately granted user.

Discovery reads labels, colors, category/order and both revisions together in
one SQL snapshot under the shared authority/configuration fence and parent locks.
It cannot pair an old category label with a newly valid meaning token. A waiting
request rechecks grants, assignment, session and lifecycle before returning data.
Invisible Jobs return 404; denied mutations on visible Jobs return 403.

A rename is safe. A missing/replaced identity, changed category (including ABA),
or stale record revision rejects with HTTP 409 and
`status_reselection_required`. Refresh the authorized choices and explicitly
reselect; never silently replace the target or revision. The same valid current
target is an attribution-preserving no-op. Definition management has no field
API route. `TestWorkflowIntentProductionAPI` and
`TestStatusIntentRenameSafeMeaningABARequiresReselection` are executable cases.
The POST success `data` contains only the committed `id` and `status_id`; it does
not reload an unrelated, legacy-role-dependent Job representation after commit.
Operation receipts and wider offline synchronization remain owned by their
dedicated API tickets; these revisions do not substitute for operation IDs.

## Pre-v1 upgrade review

An installation not yet through migration 044 must review **every** legacy status,
including familiar labels. The migration runner stops before earlier settlement
rewrites when a workflow review is needed. With the service stopped and the
operator's existing configuration:

```sh
freefsm -workflow-preflight /restricted/workflow-review.json
```

The new file is created exclusively with mode 0600. Its `evidence` inventories raw
definitions/workflows and every Job/Project/Estimate/Invoice status reference,
including archived and conversion-hidden records. Keep the evidence and snapshot
unchanged. Populate `mappings` with a string status ID for every inventoried
definition, mapped to an explicitly reviewed canonical category key. Invoice
legacy choices require a manual slot (`invoice:draft`, `invoice:invoiced`,
`invoice:sent`, or `invoice:void`); payment-derived slots cannot be manual intent.

```sh
FREEFSM_WORKFLOW_MAPPING_FILE=/restricted/workflow-review.json freefsm
```

Wrong object types, unknown categories, missing mappings and stale snapshots
reject before migration. Original evidence and reviewed choices are preserved
in `workflow_migration_evidence`, protected against update/delete/truncate.
Invoice definitions consolidated into the fixed slots remain in that evidence.
Migration 042/044 safeguards apply only when those migrations are pending;
already-recorded migrations are not rerun. Historical evidence already discarded
by an older binary cannot be reconstructed and is never invented.

Reviewed one-shot upgrades from pre-032 (and pre-013) schemas carry the reviewed
cutover marker through earlier migrations. Their old label-only status seeds are
deferred to 044, which creates any missing fixed categories with explicit
canonical meanings. No invented mapping is added to the original review, and no
existing custom label is treated as a semantic instruction. Applied migration
identities are retained. Full-runner regressions verify stale-review rejection
before intervening migrations, preserved Job/status/Customer identities, exact
original evidence, all six Invoice slots and an idempotent restart.

The preflight command currently expects the work tables to exist (schema 007+).
Earlier incomplete schemas require completing the structural migrations in a
maintenance environment before using this inventory command. Mixed-version
traffic during migration is unsupported.

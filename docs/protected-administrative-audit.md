# Protected administrative audit

Administrators open **Configuration → Protected audit** (`/settings/audit`).
`audit.view` and `audit.export` are independent sealed permissions with the
catalog's compatible View prerequisite. Directory, operational activity, other
exports, historical Role names and legacy `users.role` strings grant no access.
The public field API has no audit or export endpoints.

## Recorded evidence

The browser and versioned `protected-audit-v1` CSV use this closed column order:

```text
audit_id,accepted_at,actor_kind,actor_id,actor_identity,actor_name,event_role_id,event_role_identity,event_role_name,permission,scope,target_type,target_id,channel,outcome,host_uid,initiator_id,initiator_identity,initiator_name,device_name,session_id,operation_id,occurred_at
```

- Identity UUIDs, names and effective Role names are immutable insertion-time
  evidence, not joins to current users, Roles or sessions. Numeric IDs are
  historical references, not links or proof of the current identity.
- Migration `074_protected_audit_evidence` preserves all prior rows and grants.
  Current users/Roles acquire evidence UUIDs; **old audit rows are not backfilled**.
  Missing event-time facts remain blank. Existing, explicitly recorded API
  device/session/operation/occurrence facts are projected from their typed
  metadata without looking up today's session or person.
- The shared product audit writer captures known API session/device facts in the
  effect transaction, including actions whose domain detail only carries an
  operation ID. Session token hashes are lookup inputs, never audit output.
  Occurrence metadata must be an absolute RFC3339 instant; relative strings such
  as `now` stay unknown rather than becoming invented historical facts.
- `accepted_at` is the recorded server acceptance instant; client occurrence is
  separate and blank when not recorded. Timestamps are UTC RFC3339. Host recovery
  retains Deployment Operator/host UID attribution and does not impersonate a
  Product User. Worker events identify the system executor and separately show
  the recorded requester/initiator facts and effective Role.
- Raw audit detail JSON, request bodies, credential material, file bytes and
  removed Comment text are not part of this projection. Existing retained Comment
  inspection remains separately guarded by sealed audit View **and current
  parent View**, with its own access evidence.
- No product update, purge, anonymization or erase action exists. PostgreSQL
  rejects audit UPDATE, DELETE and TRUNCATE. Application logs are separate;
  rotating them does not modify this history. Backup/restore preserves the
  recorded identity snapshots and export artifacts rather than resolving
  historical numeric IDs against destination users.

## Filters and ordering

Permission, scope, channel, outcome and event-time Role name are exact recorded
matches; blank means all values. Event-time Role filtering never uses the actor's
current Role. Sorting is explicitly **newest ID first** or **oldest ID first**,
with stable identity ordering. Browser pages contain 25, 50 or 100 rows. Next-page
links retain filters and sorting. The Updated at value describes that read;
manual refresh obtains fresh evidence.

## Audited access and durable CSV

Every successful audit page read commits an immutable `audit.view` access event
before any evidence is rendered. Export acceptance, generation and download
commit `audit.export` evidence. Audit failure returns an error without publishing
data or a partial artifact. Denials use security logs, not fabricated successful
domain history. Ordinary browsing/search stays outside this access accounting.

**Review audit export** shows the filters, sorting, scope, fixed columns and
retention warning before **Confirm export**. The worker uses the shared durable
export service and one repeatable-read snapshot for **every matching row**, not
the current browser page. As-of time and count describe exactly that artifact.
The generation's own access event commits after snapshot selection and therefore
is not retroactively inserted into its rows.

Pending, Running, Ready and Failed are persisted. `/exports` is requester-only;
return there after navigation or an interrupted response to find accepted work.
The same operation ID and intent cannot create a duplicate export. A replacement
worker resumes an interrupted Running job, and preparation failures never mark
partial results Ready.

Ready downloads last **24 hours after Ready**, require the original requester and
current sealed View plus Export authority, and recheck under the shared authority
fence through commit. Losing either authority blocks the entire prepared
artifact, including counts/as-of metadata; no permitted subset is substituted.
An observed blocked artifact stays blocked even if authority is later restored.
Generate a new export. Downloaded copies remain outside subsequent access control.

CSV escaping follows the shared export convention: prepend one apostrophe to
formula-leading text (including behind whitespace/control characters), and to
values already starting with an apostrophe. Remove exactly that added apostrophe
to recover the original value. CSV quoting preserves commas, quotes and newlines.

## Acceptance checks

`TestProtectedAudit*` in `internal/handlers/audit_integration_test.go` exercises
production routing/middleware, real PostgreSQL, field-API exclusions, migration,
concurrent snapshots/replay, commit-time authority loss, immutable history,
access/storage faults, replacement workers, persisted browser CSV transfer and
encrypted backup/restore with colliding source/destination actor IDs.

```sh
FREEFSM_TEST_DATABASE_URL=postgres://... go test -v ./internal/handlers -run '^TestProtectedAudit' -count=1
```

Enable `BROWSER_TESTS=1`, `NODE_PATH` containing `playwright-core`, and an installed
`BROWSER_EXECUTABLE` for the persisted browser journey. Enable
`FREEFSM_BACKUP_TEST_ADMIN_URL` on a disposable owned cluster for the real restore
test. The worker-process test is a subprocess helper exercised by its parent;
its standalone skip is not an acceptance pass. Run full `make lint` and explicit
database-backed `make test` in addition to these focused checks.

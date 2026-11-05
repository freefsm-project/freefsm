# Service Entry synchronization and recovery

The website remains online-only. `internal/api/v1/reference/service-entry-client.cjs`
is an executable reference adapter over the existing protected field client and
Secret Service-backed AES-GCM store. Its production-HTTP conformance journey is
`TestServiceEntryProtectedReference`; run with a disposable PostgreSQL URL and
`FREEFSM_OFFLINE_KEY_TESTS=1`. Protection failure has no plaintext fallback.

## Read and stage

Complete `/sync/jobs` snapshots/deltas before exposing or staging cached work.
Authorized operational Assets contain Customer-period `service_entries` and a
`service_entry_context` with the exact `link_id`, `can_create`, and permitted
participant choices. Entry rows retain stable opaque IDs, Asset/Job references,
date, current summary, responsible names, revision/time and `can_revise`. Exact
editable rows also supply `link_id` and responsible user IDs. A reference to a
historical Job grants no access to its other facets. Association events/periods
are never serialized. Ordinary office Asset history retains lifetime scope;
Customer and field history remain limited to that Customer's recorded periods.

Creation requires independent Job View, Asset context, Service Entry View/Create,
current assignment to an active (unarchived) Job, its operational Asset link, a
valid service date in the original association period, nonblank summary and at
least one permitted responsible user. Completed/Canceled status does not archive
a Job. Zero or multiple entries never prevent completion.

Use `stageServiceEntry(job, asset, reviewed, dependencies)` explicitly; staging
is pending, not server acceptance. Creation `reviewed` contains `service_date`,
`summary`, `responsible_user_ids`. Revision also contains `entry_id` and the
**originally read** opaque `revision`. `submitBatch(ids)` explicitly submits.
Unrelated Job facets do not rotate an entry revision. Summary A→B→A still rotates
it: stale revision conflicts, without merge or force.

The Job, Asset, exact link/association period, occurrence, actor, acceptance and
immutable revisions are retained. Any accepted entry permanently blocks changing
the Job Customer, including after revision or completed work. Effects, blocker,
history, audit and receipt are committed together under the shared authority fence.

## Wire example

```json
{"operations":[{
  "operation_id":"87000000-0000-4000-8000-000000000001",
  "type":"service_entry.create",
  "job_id":"<original opaque Job ID>",
  "asset_id":"<original opaque Asset ID>",
  "link_id":17,
  "service_date":"2026-11-05",
  "summary":"Replaced seal",
  "responsible_user_ids":[4],
  "occurred_at":"2026-11-05T12:00:00Z",
  "depends_on":[]
}]}
```

Send to `POST /sync/operations`; revision uses `service_entry.revise` plus original
`entry_id` and `revision`. Batch targets are strings (opaque IDs or retained decimal
installed IDs). No dependency result supplies a target or revision. The common
100-operation/1MiB limits, ordering, per-operation atomicity and rate limits apply.
Direct POST `/jobs/{id}/assets/{assetID}/service-entries[/{entryID}]` retains its
existing numeric identity compatibility and also accepts synchronized opaque IDs.
GET the collection for direct rows/link; GET `/{entryID}/revisions` for authorized
immutable revisions. Local OpenAPI 3.1 defines requests, responses and errors.

## Lost response, conflict and relationship loss

1. Query `/service-entry-operations/{original-operation-id}` before retry. Its
   immutable disposition/effect identity remains available to the original
   authenticated user/client after target loss, without comparison text.
2. Retry a nonterminal operation only with its original ID and exact intent,
   respecting retry timing. Same-intent replay cannot duplicate an accepted effect;
   changed intent under the old ID is rejected.
3. Terminal `service_entry_conflict` requires current authorized reread and explicit
   review. Call `repairServiceEntry(oldID, reviewed, dependencies)` to create a new
   ID. A failed dependency remains failed; repair each descendant explicitly with
   its own new ID and reviewed dependency list. Never silently refresh revisions,
   replace targets, or resubmit successful unrelated operations.
4. Assignment, grant, Asset link, Customer relationship or archive loss rejects
   original intent. Purge inaccessible content, including pending comparison text;
   do not retarget to a later link, another Customer, Job or Asset. Discard or start
   a separately reviewed new intent where currently authorized. Known IDs and a
   prior offline lease confer no authority.
5. Ordinary revocation/disablement purges on contact. Lease expiry locks exposure
   and staging. Restore uses the inherited locked review flow: fresh authentication,
   current-authority synchronization and explicit receipt reconciliation; missing
   post-backup receipts remain uncertain, never automatic replay. Logout destroys
   the owned key/cache.

## Coverage observation

`lease_coverage` is only current covered Yes/No for this Job Customer, with `as_of`.
Display **Last synced: Yes/No — as of …** while offline. Never calculate fresh
coverage from a cached date. No Lease ID, terms, rates, dates, Files, other Assets
or Lease history are included. The adapter inherits complete Job-scoped observation
invalidation before refresh, including policy/network failures and restart.

## Compatibility / change record

This slice adds synchronized editing references/capabilities, opaque Asset/Entry
route aliases, Service Entry batch members/terminal dependency receipts and the
encrypted reference adapter. Installed identities/history and existing routes are
retained; no grants are backfilled. Clients ignore additive response fields and
handle unknown error codes through explicit review. Migration125 expands receipt
dispositions without rewriting installed rows.

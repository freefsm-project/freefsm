# Comments, replies and removal — #45

## Runtime contract

The web Comment widget on Customer, Project, Job, Asset, Estimate and Invoice
uses `authorization.Service` for reads and commands. Parent basic View is checked
independently from `comment.add` and `comment.remove`; parent Edit is not used.
Assigned scope intersects the current active Job assignment. Own removal always
intersects current parent access. Context and financial-only access cannot open
master collaboration. Contact, Location, Item and Time Entry have no Comment
routes. Lease is not an available parent in this integration base; its owning
ticket must connect its basic view and parent authorization when introduced.

Comments contain nonblank plain text, at most 10,000 Unicode code points. Line
breaks are preserved and HTML is escaped. Lists sort by creation time and ID,
oldest first. There is no edit endpoint. Replies name an exact open top-level
Comment on the exact parent; replies to replies are rejected. Removal retains
author, original time, remover, removal time and reply relationships. Top-level
removal closes the thread; removing a reply leaves the thread open.

Ordinary projections return empty content for removed Comments. Historical
activity preview bytes remain in storage but are suppressed in ordinary feeds:
old events did not record a stable Comment ID, so associating them with a removal
would invent history. New activity events contain only the Comment ID. The web
`GET /{parents}/{id}/comments/{cid}/audit` endpoint returns retained text as
`text/plain`, requires both sealed `audit.view/S` and current parent View, and
audits the read. It is not a field API endpoint.

Mutations use the shared authorization transaction mutex, lock the exact parent,
and lock applicable Job/assignment and thread records. Session/identity are
rechecked before commit. Effects, immutable audit, ordinary activity and operation
receipts commit together. Audit records include effective Role, permission/scope,
parent, Comment ID, operation ID, channel, session ID when present and supplied
occurrence time. Database/audit failures roll back and remain retryable.

Migration `058_comment_threads.up.sql` adds nullable reply/removal fields and an
immutable receipt table. It preserves existing IDs, content, authors and both
timestamps, including historical text longer than today's creation limit. It
does not change grants or invent historical removals/replies. Existing conversion
moves the same Comment rows, retaining these fields and links; pending operations
against a now-hidden original parent reject instead of following the move.

## Field API and OFF-05 reference seam

Paths below are relative to `/api/v1`, using existing authenticated mobile bearer
sessions. Eligible field parents are Jobs and Job-linked Estimates/Invoices.
Customer-only documents and master maintenance do not enter the field API.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/{jobs,estimates,invoices}/{id}/comments` | Currently authorized ordinary projection and action capabilities |
| POST | `/{jobs,estimates,invoices}/{id}/comments` | Add top-level Comment or one-level reply, returning a receipt |
| POST | `/{jobs,estimates,invoices}/{id}/comments/{cid}/remove` | Own removal only, including already-removed no-op |
| GET | `/comment-operations/{operation_id}` | Nonsensitive terminal receipt for the same actor/client |

Create/reply body:

```json
{
  "operation_id": "04500000-0000-4000-8000-000000000001",
  "content": "First line\nSecond line",
  "reply_to": 123,
  "occurred_at": "2026-10-12T01:00:00Z"
}
```

Omit `reply_to` for a top-level Comment. `occurred_at` is optional and cannot be
future; it does not establish authority, ordering or deduplication. Remove accepts
the operation ID and optional occurrence time, with no content/reply target.
Unknown JSON fields reject. Successful responses use the existing `data` envelope:

```json
{"data":{"operation_id":"04500000-0000-4000-8000-000000000001","comment_id":124,"code":"accepted","accepted_at":"2026-10-12T01:01:00Z"}}
```

Operation IDs bind actor, authenticated client, exact parent, action and intent.
Same-intent replay returns the original receipt without another effect/audit.
Changed intent is `409 comment_conflict`. Terminal rejection codes are
`invalid_comment` (400), `forbidden` (403), `not_found` (404) and
`comment_conflict` (409); they remain queryable without Comment content. Storage
failure is `503 retryable_failure`, with no terminal receipt. Receipt lookup and
same-intent replay survive parent access loss but reveal no text or parent
representation. New operations still require current parent access. Receipts have
no automatic expiry. Web forms also carry operation IDs for duplicate submissions.

The executable `internal/handlers/testdata/browser/comment-reference-harness.cjs`
stages exact intent in a disposable fixture file, reloads it, simulates a lost
response, queries the receipt, replays once, removes twice and verifies a queued
reply terminally rejects a closed thread. This is the ticket's reference harness;
the broader offline lease/cache/snapshot/sync platform belongs to its own slices.

## Verification

All commands ran in `/tmp/opencode/freefsm-v1-45` with `GOMAXPROCS=2 GOFLAGS=-p=2`.
The unique disposable database was created with:

```sh
createdb -h 127.0.0.1 -p 55419 -U adam freefsm_v1_ticket_45
make lint
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_45?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55419/postgres?sslmode=disable' \
make test
NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_45?sslmode=disable' \
go test -race ./internal/handlers -run '^TestCommentsPersistedBrowser$' -count=1 -v
```

Lint, full race suite and persisted Chromium passed. Full passing suite output:
`/home/adam/.local/share/opencode/tool-output/tool_130275246001d4hH7RBYbPPxxk`.
Unchanged packages used Go's cache on the final full-suite rerun; real backup and
restore integration had executed successfully in the preceding run. The first
full run exposed the old Ent-only handler fixture's missing Comment schema/grants
and obsolete physical-deletion expectation; these were corrected explicitly.

Meaningful red→green slices reproduced independent-grant denial, accepted nested
replies, missing API routes/create-once behavior, legacy removed-preview leakage,
All-View/Assigned-Add scope rejection, transient failures mislabeled as conflicts,
missing API device attribution and missing ordinary attributable Comment activity.

| Acceptance area | Evidence |
| --- | --- |
| All currently available parents, unsupported standalone parents, archived denial, independent Own removal | `TestCommentsAllAvailableParentsAndIndependentRemovalHTTP` |
| 10,000 Unicode characters, line breaks, 10,001 rejection, no parent Edit | `TestCommentsIndependentGrantsAndCharacterBoundaryHTTP` |
| Exact top-level thread, nested/closed rejection, reply removal vs top-level removal, retained audit text, feed redaction | `TestCommentsRepliesRemovalAndRetainedTextHTTP` |
| Atomic failure, same-ID retry, reply/removal concurrency | `TestCommentsAtomicAuditFailureAndConcurrentClosureHTTP` |
| Create-once, changed-intent rejection, own already-removed no-op, API device attribution | `TestCommentsAPICreateOnceAndOriginalThread` |
| Assigned scope, Context exclusion, lost-response receipt after scope loss, Own no bypass | `TestCommentsAssignedScopeAndLostResponseHTTP` |
| Grant revocation while request waits for transaction lock | `TestCommentsRecheckGrantWhileWaitingToCommitHTTP` |
| Conversion preserves thread; stale original parent never retargets | `TestCommentsDoNotFollowConvertedParentHTTP` |
| Pre-058 history/grants preserved, restart preserves replies/removal | `TestCommentsUpgradePreservesRecordedFacts` |
| Persistent pending intent and terminal reconciliation | `TestCommentsOfflineReferenceHarness` |
| Persisted parent-view browser create/reply/remove/reload, escaped plain text | `TestCommentsPersistedBrowser` |

Full-suite skips: unsupported PostgreSQL-major negative case (cluster is 18),
subprocess-only helper entry points, optional nginx scale and optional browser
tests. The Comment browser test was then explicitly run and passed. Installation,
Customer-permission, backup-template and document-line browser journeys were not
rerun separately for this ticket. The contract's `/usr/bin/chromium` was absent;
the installed `/usr/bin/chromium-browser` worked. No skipped test is counted as a
pass. Lease-parent wiring awaits the Lease slice; no Lease behavior is claimed.

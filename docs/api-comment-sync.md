# Comment and reply synchronization

The API and encrypted native reference support Comments on active Jobs and
independently authorized active Job-linked Estimates/Invoices. The website stays
online-only. Existing collaboration authorization, immutable receipts and the
common operation registry own acceptance; offline leases never authorize writes.

## Coherent snapshots and deltas

`GET /api/v1/sync/jobs` now includes `comments` and `document_comments` on each
complete Job replacement. Document entries expose only `parent_type`, opaque
`id`, and their Comments projection, not commercial fields. Independent parent
View filters documents before projection. Each collection exposes `can_add`, an
opaque `eligibility_revision`, and
oldest-first entries with stable opaque `id`/`thread_id`, original author identity,
author name, creation time, text, optional `reply_to`, removal attribution and
`can_reply`/`can_remove`. These capabilities are advisory, never authority.

IDs reuse the existing sync alias registry and preserve installed identities.
The legacy direct Comments routes and receipts retain numeric installed IDs;
batch targets and Comment references accept their stable sync aliases or decimal
strings. Never infer an identity from a list position or follow conversion.

Removed entries have empty text and retain their placeholders/attribution.
Removing a root preserves replies but closes further replies; removing a reply
does not close its root. Retained text is available only through separately
authorized online audit with current parent access. Old materialized pages that
could disclose newly removed text or a lost document relationship require purge
and resnapshot. Fresh deltas carry current placeholders. Apply complete Job
replacements atomically, including omitted document facets.

## Ordered immutable intent

```json
{"operations":[{"operation_id":"84000000-0000-4000-8000-000000000001","type":"comment.add","target":"original-parent-sync-id","depends_on":[],"comment":{"parent_type":"job","parent_revision":"original-eligibility-revision","content":"First line\nSecond line","reply_to":"original-open-root-sync-id"}}]}
```

Omit `reply_to` for a root. `parent_type` is `job`, `estimate` or `invoice`.
For `comment.remove`, supply `comment: {"parent_type":"job",
"parent_revision":"original-eligibility-revision",
"remove_id":"original-comment-sync-id"}` with no content/reply reference.
Optional RFC3339 `occurred_at` is nonfuture, microsecond-precision evidence,
not ordering, authority or deduplication. There is no edit operation.

Text is nonblank plain text, preserving line breaks, at most10,000 Unicode code
points; exactly10,000 passes,10,001 rejects. Replies target one open root on the
exact original parent. Unrelated Job/document edits need no revision comparison.
The API permits own removal only, even with broad office Remove grants; Own
never bypasses current parent View, current scope or active parent/Job eligibility.
Already-removed own removal succeeds without changing removal actor/time/history.

The existing100-operation/1MiB decoded-envelope limits and complete transport
preflight apply, including null/nonstring dependency rejection before any effect.
Every operation separately commits effect, audit, history and receipt atomically.
Dependencies use the existing deployment/user/client-bound owning receipts.
Failure blocks descendants, while unrelated Comments/Notes remain eligible.

Capture `eligibility_revision` at staging and send it unchanged as
`parent_revision`. It binds the exact parent's current lifecycle generation,
original Job relationship, and the acting user's assignment identity. Conversion
and reversion, archive and restore, relink away/back, or own unassign/reassign
cannot rehabilitate an old unsubmitted intent merely because IDs return. Ordinary
title, Notes, workflow Status and commercial-content edits do not change this
eligibility witness. A missing or stale witness gives terminal409
`comment_parent_changed`, with no Comment/audit effect; explicitly review current
scope and create a NEW operation ID. Already-terminal immutable replay is recovered
before this guard, including older receipts whose intent had no witness.

Direct API Comment commands require the same witness for a new effect. It remains
optional in the wire schema so historical already-terminal intents can replay
with their original hashes; omission never permits a new API effect. Website
online-only forms retain their existing current-authority contract.

`data.results` contains ordered receipts/errors. Receipt lookup is
`GET /api/v1/comment-operations/{operation}` for the original authenticated
user/client, even after target loss. Receipts contain only operation ID, code,
acceptance time and optional stable installed Comment effect ID. Same-ID replay
returns the original receipt; changed batch intent returns
`409 operation_intent_changed`. Terminal codes include `invalid_comment`400,
`not_found`404, `forbidden`403, `comment_conflict`409, `comment_parent_changed`409,
`invalid_dependency`400 and `failed_dependency`409. Transient storage/audit failure
has no receipt and remains retryable under the same ID after receipt reconciliation.

## Protected reference and reviewed recovery

Use the existing `offline-notes.cjs` executable and protected store. After online
authentication and complete synchronization:

- `stage-comment`: supply `job`, explicit `reviewed` object containing
  `parent_type`, `target`, and either `content`/optional `reply_to` or `remove_id`,
  plus an explicit `depends_on` array. Returns a new pending operation UUID.
- `submit-batch`: explicitly submit selected pending IDs in order. Each becomes
  durably unknown before transport. Nothing submits on restart/synchronization.
- `reconcile`: query each original receipt after interruption or lost response.
  A confirmed ordinary miss permits explicit same-ID unchanged retry, subject to
  retry delay. Receipt absence after restore remains uncertainty, never failure.
  Receipt-first contact durably locks cache/pending disclosure and returns
  `requires_synchronization: true`; reconcile the group, then explicitly complete
  a fresh synchronization before view, staging, repair or submission. An accepted
  safe receipt cannot unlock scope-lost Comment text. Failed withholding writes
  retain the existing quarantine marker across fresh processes/access restoration.
- `repair-comment`: supply the terminal original `operation_id`, reviewed intent
  including the explicitly reviewed current `parent_revision`, and explicit
  reviewed dependencies. Creates a NEW ID on the same original
  parent/thread/action, using current synchronized capabilities. It never repairs
  descendants automatically. Accepted intent cannot be ordinarily repaired.
- `discard`: remove local pending work. To choose another parent/thread, review
  that currently authorized target and explicitly stage a separate new intent;
  no repair, conversion or relationship lookup silently moves pending text.

Comment rejection also withholds the stale comparison/cache until explicit fresh
synchronization. All batch members still receive their independent outcomes;
unrelated committed work remains accepted and original unprocessed IDs are kept.

Scope, conversion, archive and relationship loss purge inaccessible pending
content and snapshots on contact. Fresh sync also checks the original document
within the original Job, not merely that the Job remains visible. Removed text
is withheld after refreshing placeholders. Purges use the existing durable
quarantine boundary; failed persistence cannot revive older authority on restart.
Ordinary denial/logout and restore-locked review remain distinct. Restore requires
fresh authentication, complete current sync and explicit outcome reconciliation
before reviewed new-ID repair. No post-backup history is fabricated.

## Conformance

The locally served OpenAPI3.1 describes batch variants, snapshots, direct Comment
routes, receipts and errors. Owning tests exercise production HTTP/migrated
PostgreSQL and the actual Secret Service encrypted reference, including bounds,
closure, persisted prefix interruption, receipt-first continuation, dependency
repair, scope/relationship purge, and served-schema validation. Existing Comment
browser and installed-history tests remain regression gates. Scoped Chromium and
native key-service evidence are not a cross-device release certification.

## Installed preservation

Migration087_comment_eligibility adds current protocol metadata keyed by existing
parent type/ID. It neither alters business rows nor reconstructs historical actors,
times or transitions, and changes no grants. UPDATE-only lifecycle triggers rotate
that current revision atomically with actual parent changes. Authorized reads
allocate missing current baselines, like existing sync aliases; no INSERT trigger
interferes with exact backup/restore COPY. Existing identifiers, Comment facts,
direct-command hashes and non-Comment batch bindings remain intact.

# Offline File publication and reconciliation

File bytes use the [scoped transfer protocol](api-file-transfers.md). Finishing
or verifying a transfer **does not publish an attachment**. `POST
/api/v1/sync/operations` adds these closed metadata-only variants:

```json
{"operations":[{"operation_id":"86000000-0000-4000-8000-000000000001","type":"file.publish","target":"86000000-0000-4000-8000-000000000002","object_type":"job","object_id":42,"depends_on":[]}]}
```

- `file.publish`: `target` is a fully validated transfer belonging to the current
  user/client and **exact original** `object_type`/`object_id`. Allowed parents are
  Jobs and Job-linked Estimates/Invoices. The operation ID is the create-once
  publication identity. Name, size, digest and bytes come from that transfer.
- `file.rename`: `target` is the existing File ID as a decimal string; supply the
  exact original parent, reviewed opaque `revision`, and complete `name` including
  its unchanged extension.
- `file.remove`: same target/parent/revision, with no `name` property.
- Each carries `depends_on` and optional RFC3339 `occurred_at`. The entire envelope
  is strictly preflighted before dispatch. Unknown properties (including bytes),
  null dependencies and shape errors reject the entire envelope without effects.

The 100-operation/1 MiB JSON limits apply to this metadata, not to transfer bytes.
Operations use the existing File service, common operation admission and immutable
receipts. Dependencies are checked at commit after same-intent receipt recovery;
earlier or known successful bound receipts alone qualify. Failed descendants are
blocked, while unrelated Notes and File operations continue. Byte validation,
storage, current authority, original parent and audit must all succeed before an
ordinary File and its collaboration history are accepted.

## Revision and authority rules

Field Rename/Remove are **original-uploader only**, even with office grants, and
always require current parent access. Rename changes the display name only;
duplicate names are permitted and replacement is a new publication/File.
Current-name rename and already-removed-own-File removal are no-ops preserving
effect attribution; other stale or ABA revisions conflict. Each command still
receives its own immutable disposition. No force, merge, implicit parent-following
or target/revision rewriting is supported.

Removal retains uploader/name/size, bytes and removal actor/acceptance time, while
ordinary content access is denied. It does not mutate immutable Delivery snapshots
or promise restore or secure erasure. Archived, hidden-converted, moved or
inaccessible original parents require manual review; a newly accessible parent
does not authorize replay against that different parent.

## Receipt-first recovery

`GET /api/v1/file-operations/{operation}` returns only safe disposition, acceptance
time and identity after scope loss, never names, bytes, paths or comparisons.
Accepted receipts include `effect_id`, the exact decimal-string File identity;
legacy `file_id` remains for compatibility and must not be used through a lossy
JavaScript numeric conversion. `effect_id` is absent on rejections.

Same-ID/same-intent batch replay returns the recorded effect. Changed intent is
rejected without borrowing a previous success to unblock descendants. Transient
audit/storage failures remain same-ID retryable after receipt reconciliation;
terminal correction and each blocked descendant need separately reviewed new IDs.
Do not switch between direct and batch publication representations to retry:
their intent bindings differ. Query the original receipt instead.

A new reviewed batch publication may reference the same still-authorized verified
transfer with a new operation ID. Changing parents requires a newly scoped transfer
and new publication intent, never transfer retargeting. The legacy transfer's
`publication_id` still governs its direct `/publish` endpoint; the explicit batch
operation ID governs batch publication.

## Encrypted executable reference

The existing `internal/api/v1/reference/offline-notes.cjs` executable now uses
`OfflineFiles`, which extends the same protected-store/lease/quarantine lifecycle.
It accepts JSON on stdin, never credentials in argv:

- `stage-file-publication`: cached `job`, `name`, `bytes_base64`, explicit
  `depends_on`, and optional reviewed `parent` from the snapshot's `file_parents`.
  Bytes, immutable transfer/chunk operation IDs and original intent are encrypted.
- `transfer-file`: original `operation_id`; explicitly sends/resumes separate raw
  chunks, checks discovered limits and verifies the digest. Completion is labelled
  `transferred_not_published`.
- `reconcile-file-transfer`: publication `operation_id` and original
  `transport_operation_id`; queries only that start/chunk receipt. Safe lookup is
  independent of the offline lease and parent visibility, but requires a current
  authenticated session. Receipt contact withholds cached projections (including
  when the lookup fails): synchronize before another transfer attempt.
- `stage-file-mutation`: cached `job`, exact `file_id`, `kind` (`rename`/`remove`),
  rename `name` and explicit `depends_on`; takes the currently reviewed revision.
- `submit-batch` and `reconcile`: existing receipt-first, persisted-unknown flow.
- `repair-file`: original ID, explicit `depends_on` and `review` containing current
  `revision` (and new rename `name`), or the exact reviewed publication parent and
  optional explicitly corrected publication `name`.
  Creates new IDs without changing the original target. Publication repair stages
  a new scoped transfer. Restored uncertainty first requires `acknowledge-uncertain`.

Each transport command has its own durable pending/unknown/accepted/terminal
disposition, separate from the publication receipt. Unknown is encrypted before
network send; a chunk's UUID, offset and length are staged with the original exact
bytes and never resized under that UUID when discovery limits change. An explicit
`transfer-file` encountering unknown transport first queries its original receipt
and returns `requires_synchronization`; it sends no bytes in that recovery call.
After synchronization, an accepted operation uses authorized transfer progress,
never write replay. Only a successful same-deployment receipt miss permits an
unchanged retry. Unavailable or malformed lookup retains unknown. Retry deadlines
from typed errors persist across restart, including discovery/progress/verification
failures. Terminal transport or verification failures require explicit reviewed
repair with new publication, transfer and operation IDs. Verification is a fresh
observation, not a receipt-bearing publication or a durable integrity certificate.

Older encrypted queues lacking segment lengths reconcile their recorded UUIDs;
an unreceived historical chunk requires review rather than guessing its shape.
Scope loss retains only safe transport identities/dispositions/receipts, removing
bytes, names, segment shape and original-parent details. Restore receipt absence
never authorizes replay; transport acceptance alone does not resolve publication
uncertainty or bypass the existing explicit restore review.

Ordinary scope loss purges inaccessible names/bytes/intent; failed persistence
quarantines the old ciphertext. Restore locks pending intent until fresh auth,
complete current-authority sync and explicit outcome reconciliation. Neither
restart, authentication nor sync submits effects. Accepted publication staging
bytes are discarded; this cache is not a backup. The native reference refuses
parent numeric IDs outside JavaScript's safe integer range rather than rounding
them; File targets and accepted receipt identities remain exact strings.

See [File snapshots](api-file-snapshots.md), [batches](api-operation-batches.md) and
the local OpenAPI `BatchFileOperation`, `SyncFile`, `SyncFileParent` and
`FileReceiptEnvelope` schemas. Website behavior remains online-only.

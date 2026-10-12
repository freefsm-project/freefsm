# File publication and storage (#46)

## Publication boundary

Uploads stage bytes before creating an ordinary File. The server measures the
stream, checks its extension against detected content, verifies a supplied
SHA-256, and synchronizes the file and containing directories. Publication then
locks current authorization and the original parent, inserts the File and its
publication identity, and records protected audit in one transaction.

Each queued browser File has a stable UUID publication ID. A lost response can
be retried with the same ID and identical intent without creating another File.
Different intent with that ID conflicts. Replacement uses a new publication ID
and therefore a new File identity; duplicate display names are allowed.

An uncertain database commit must never cause deletion of possibly committed
bytes. Failed attempts can consequently leave unreferenced durable files. They
are not ordinary attachments. Do not remove them merely because a request failed;
reconcile the publication first. Capacity planning includes staged transfers and
unreferenced files.

## Limits and formats

`FREEFSM_MAX_UPLOAD_SIZE` is a byte count, default **26,214,400 (25 MiB)**.
Authenticated `GET /files/limits` and `GET /api/v1/files/limits` expose bounds.
Browser batches contain at most ten Files, with independent transfer progress,
publication outcomes, and an explicit **Retry failed files** action. Successful
items are retained while failures are retried.

Accepted extensions: PNG, JPG/JPEG, GIF, WebP, PDF, TXT, CSV, DOC/XLS/PPT,
DOCX/XLSX/PPTX, ODT/ODS, ZIP, and JSON. Content detection uses the pinned
`github.com/gabriel-vasile/mimetype` library, with structural compound-file
validation for legacy Office using `github.com/richardlehane/mscfb`. Legacy Office
identification follows allocation chains to the actual root streams and their
Word FIB, Excel BOF, or PowerPoint record headers; it does not assume their
directory resides within a small prefix. Streams must be fully readable within
the physical file's bounds. ZIP containers must have a readable central directory
and JSON must parse. ZIP entries are never extracted. This is
content-type validation, not a malware-scanning or active-content-removal claim.

## Access and lifecycle

Basic parent View bounds File metadata. Download/Preview, Upload, Rename, and
Remove have independent grants. Own Rename/Remove retain the original uploader
anchor and cannot bypass parent access. Closed Job Context does not expose master
File collections. File actions do not use legacy Role labels.

Rename preserves the exact extension and durable bytes. Web mutations compare
the displayed revision; the API uses an opaque revision token. A rename to the
current name and removal of an already removed File are no-ops. Parent moves
advance the revision; API mutations additionally require the original parent.
Lost original parents require manual recovery, never retargeting pending intent.

Removal retains the row, uploader, name, size, bytes, removing actor, and time.
Ordinary preview/download is denied. No restore endpoint or secure-erasure claim
is offered. Existing Delivery PDF snapshots are independent immutable data.

## Resumable API

Paths below are relative to `/api/v1`; all require the current mobile bearer.
The API accepts Job Files and Job-linked Estimate/Invoice Files only. It does
not expand field API access to Customer-only documents or master collections.

1. `POST /file-transfers` with `id` (transfer UUID), `publication_id` (stable
   publication UUID), `object_type`, `object_id`, `name`, `size`, and `sha256`
   (lowercase hexadecimal SHA-256 of exact bytes), plus optional `occurred_at`
   (a nonfuture RFC3339 client occurrence, stored at PostgreSQL microsecond
   precision). The publication ID is its operation ID, independent of retries or
   transfer-attempt IDs. Changing the occurrence changes publication intent.
2. `PUT /file-transfers/{id}/chunks?offset=N` sends raw bytes, at most **1 MiB**.
   Exact chunk replay is accepted; changed bytes or an incorrect offset conflict.
3. `GET /file-transfers/{id}` returns current progress. Existence/resume is scoped
   to the user and mobile session and rechecks current parent/Upload authority.
   There is no global digest-existence endpoint.
4. `POST /file-transfers/{id}/publish` explicitly publishes after complete transfer
   and digest/content validation. Completion of step 2 never publishes a File.
   Repeated publication resolves the same identity.
5. `GET /files/{id}` downloads; `GET /files/{id}/metadata` returns metadata and
   the opaque revision token without exposing a filesystem path.
6. `POST /files/{id}/rename` requires `operation_id` (nonzero client UUID), `name`,
   `revision`, `object_type`, `object_id`, with optional `occurred_at`.
   `POST /files/{id}/remove` requires its own `operation_id`, original parent and
   revision, with optional `occurred_at` (and no `name`).
   The field API restricts these two actions to the original uploader.

API File effects atomically record the authenticated session ID and a snapshot of
its device label, operation ID, occurrence, database-clock acceptance time and
whether occurrence came from the client or server. Neither bearer tokens nor
their hashes are audit details. Device identity and acceptance time cannot be
supplied by the mutation payload. Omitted occurrence records the server acceptance
instant with source `server`, not an invented offline occurrence. Session deletion
does not erase that immutable attribution.

Successful rename/removal operations bind their original intent and device in
`file_mutation_operations`, including desired-state no-ops. Exact replay does not
reapply an effect or reattribute its audit. Reuse with a different intent, device,
or File action conflicts; reviewed replacement intent needs a new operation ID.
Publication and mutation IDs cannot be reused across those two action types.
Audit/storage failure rolls back both effect and operation evidence, leaving the
same operation ID retryable. This is File-specific acceptance evidence, not the
broader synchronization/receipt-query protocol.

JSON responses use the existing `{ "data": ... }` envelope. Invalid content is
400; invisible resources 404; denied actions 403; revision/intent/offset/incomplete
publication conflicts 409. Storage failures do not acknowledge publication.
After a mutation, refresh metadata before preparing another mutation.

## Upgrade and recovery review

Migration `058_file_publication.up.sql` preserves existing File IDs, recorded
fields, paths, and relationships. A NULL legacy digest means **unrecorded**, not
verified. The read-only protected-Administrator endpoint
`GET /files/storage-review` reports missing/unreadable bytes, size/digest/type
discrepancies, and unrecorded legacy digests, without rewriting upload history or
silently discarding attachments. Review the report before and after an upgrade
and after a recovery drill.

Migration `070_file_api_attribution.up.sql` adds nullable transfer occurrence and
immutable successful-mutation evidence. It does not rewrite existing audits or
invent historical device, operation, or occurrence facts. The upgrade regression
compares pre/post transfer and audit records explicitly.

The existing encrypted backup inventory includes removed File bytes; PostgreSQL
backup includes publication IDs, revisions, removal metadata, transfer chunks,
protected audit, and existing Delivery snapshots. The cross-root round-trip test
uses the real migrated schema and checks those removed-File facts and an existing
Delivery PDF snapshot.

## Follow-up acceptance after integration `521de47`

The API attribution omission and legacy Office evidence gap identified by the
integration coordinator are repaired. Real DOC/XLS/PPT binaries were exported
from authored flat OpenDocument sources with LibreOffice and independently opened
back into flat OpenDocument to verify paragraphs, worksheet values and slide text.
Sources, binaries, regeneration instructions and hashes are under
`internal/handlers/testdata/files/legacy-office/`.

HTTP tests cover all three valid binaries, every cross-extension pairing, text
masquerades, truncated containers, media types and exact-byte downloads. The
persisted browser runs production static scripts and CSRF middleware: an oversized
batch sends no request; a three-format batch retains two successes while retrying
one lost successful response; mismatches reject; rename retains exact bytes and
identity; removal retains attribution and denies subsequent download. This also
exposed and repaired a second, identity-less HTMX form submission alongside the
enhanced upload queue. Only the queue submits an enhanced File form now.

API tests prove all upload/rename/remove attribution, optional client occurrence,
invalid/future/forged attribution rejection, replay and changed-intent/device
conflicts, protected-audit rollback with same-ID retry, no duplicate attribution,
and retained attribution after revocation. Existing File publication, permission,
concurrency, migration and encrypted backup checks remain acceptance seams.

## Downstream integration ownership

- The base integration branch has no Lease schema, object directory support, or
  Lease basic view. Lease File UI acceptance is blocked on that domain landing.
- Additional captured Delivery File attachments are not implemented in the base
  Delivery module. The File removal implementation preserves bytes and metadata,
  but a full extra-attachment Delivery journey still needs #56 integration.
- The new File API does not supply the general operation-receipt/offline-sync
  protocol owned by the broader API tickets. Publication identities and
  revision/parent conflict controls here are durable primitives for that work.

Authenticated generated-PDF permissions are independent of File Upload following
the merged #25 integration. Available-parent #46 behavior and its identified
own-scope gaps are verified; this does not certify downstream Lease, Delivery-extra
attachment or general offline-sync journeys, or the entire v1 release matrix.

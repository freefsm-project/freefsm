# Resumable File transfer protocol

The local `/api/v1/openapi.json` is the v1 contract. Pair through hosted browser
approval first. All File endpoints require the resulting opaque bearer; public
discovery does not expose File limits, parents, digests or transfer identities.
The website remains online-only. Transfer is not File publication.

## Upload and resume

1. Read authenticated `GET /api/v1/files/limits`. The per-File default is
   26,214,400 bytes (25 MiB); `FREEFSM_MAX_UPLOAD_SIZE` controls the limit in bytes.
   Use the discovered value, not a compiled client constant. Each request chunk
   is 1–1,048,576 bytes. A reference-client queue contains at most 10 Files.
2. Compute SHA-256 over the **exact bytes** without text/newline/image
   normalization. Start `POST /file-transfers` with a new nonzero UUID `id`,
    distinct publication-operation UUID `publication_id`, original `object_type`
    and `object_id`, `name`, exact positive `size` and lowercase hex `sha256`.
    Supply an independent client-generated UUID in the `Operation-ID` header.
    This start operation ID is neither the transfer `id` nor `publication_id`.
   Optional `occurred_at` is an offset-bearing RFC3339 instant, never in the future.
   Omit optional values rather than sending `null`. If supplied, initial
   `transferred` must be `0` and `verified` must be `false`. Unknown parent types
   and nonpositive parent IDs are nonretryable 400 input errors; a well-formed
   inaccessible Job or document remains a nondisclosing 404.
3. Send consecutive `PUT /file-transfers/{id}/chunks?offset=N` requests with raw
    bytes and a new independent `Operation-ID` UUID for each new chunk intent.
    The returned `transferred` is durable transport progress, **not**
   verified completion or publication. Retry an uncertain chunk at its original
   offset with exactly its original bytes and length. Gaps, overlaps with a
    different segment, changed bytes and oversize chunks reject. Stage the operation
    UUID with the exact offset, bytes and length before sending; an unchanged retry
    retains that same header, even if the response was lost. Repaired intent uses
    a new operation UUID, never a reused resource or publication identity.
4. After an uncertain outcome, first reconcile the staged start/chunk operation
   with `GET /file-transfer-operations/{operation}`. Its envelope is
   `data: {operation_id, transfer_id, code, accepted_at}` (a rejected operation may
   omit `transfer_id`). This safe receipt remains available after parent access
   loss; it is not a transfer representation or permission to resume. An accepted
   receipt requires no write replay. A terminal rejection requires explicit
   review and a new operation UUID for repaired intent. An unavailable receipt
   lookup leaves the outcome unknown: do not blindly retry. A successful lookup
   reporting no receipt permits an explicit unchanged-intent retry with the same
   operation header.
   `GET /file-transfers/{id}` separately discovers the currently authorized durable
   offset. Preserve the original File bytes and segment boundaries. Never reuse
   an ID for changed intent or silently retarget another parent.
5. Call `POST /file-transfers/{id}/verify`. Incomplete content conflicts. Complete
   content is reread and checked against the digest and the existing File
   content-extension validator. A successful response has `verified:true`.
   Verification is a fresh observation, not a persistent certificate; ordinary
   transfer status has `verified:false`. Reverify after retry/restart.

Verification accepts PNG, JPEG, GIF, WebP, PDF, text, CSV, legacy/OpenXML Office,
ODT, ODS, ZIP and JSON according to the shared File service. ZIP is inspected,
never extracted. Invalid formats and mismatched digests cannot be published.
Temporary validation files are restricted and removed after the request.

Transfer rows and bounded chunks live transactionally in PostgreSQL, survive
server restart and participate in the existing backup. There is no in-memory
resume dependency. No time-based transfer deletion is promised: resumability is
bounded by current session/account/parent authority and the current operator
limit. Session expiry or revocation grants no recovery exception.

## Scoping and publication

Transfers belong to the authenticated user **and paired session**, and their
original parent is immutable. Current parent View and independent File Upload
are required for every start, resume, verification and publication, and for the
transfer representation. Receipt lookup returns only the authenticated caller's
scoped nonsensitive operation outcome, including after parent loss. The API is
Job-bound (Job or Job-linked Estimate/Invoice), even for an Administrator.
Known transfer IDs and digests grant no authority. There is no global digest
index or cross-user existence endpoint.
Transfer and chunk keys are caller/session-scoped: even starting the same UUID
in another caller's namespace creates an independent transfer instead of
revealing a collision. Migration `075_file_transfer_scope` preserves existing
UUIDs, parent links and bytes, copying each chunk's recorded transfer owner into
its composite key; it grants no permissions and fabricates no historical facts.
Within a caller's namespace, the stored original parent's current authority is
checked before reporting changed-intent conflicts, even when the request names
another authorized parent. Lost original-parent visibility returns 404, not a
collision signal or permission to retarget the transfer.

Migration identities are full names, not numeric prefixes. The independent
`075_file_transfer_scope` and `075_job_title_whitespace` retain their installed
names and run in full-name lexical order after `074_job_planning`. An installation
that already applied either source branch runs only its missing migrations;
renaming either 075 would incorrectly replay an already-applied schema change.

`POST /file-transfers/lookup` accepts `object_type`, `object_id` and `sha256`.
Only a complete transfer belonging to the caller's paired session and exact
original parent can match. It is verified afresh before returning its original
identity; a miss is 404. Reuse that identity only for its unchanged intent. This
does not clone bytes or create a new publication for a different name/parent.

## Download integrity

`GET /files/{id}` requires current parent View and independent File Download.
It verifies a restricted snapshot of the authoritative File against its recorded
digest before any successful response. `Repr-Digest: sha-256=:BASE64:` covers the
entire representation, including on 206 responses. A strong digest `ETag` supports
`Range` with `If-Range`; clients verify the final reassembled bytes against the
digest. An If-Range mismatch sends the complete representation: discard old
partial bytes. A changed recorded digest, missing/short storage or snapshot
failure returns an explicit error. No successful empty substitute is returned.
For installed Files without a recorded digest, the response digest is an
observation of current bytes only; the database's historical NULL is preserved.
Exact configured CORS origins permit Range/If-Range and expose integrity headers.

Transport completion creates no ordinary File, attachment, activity or
collaboration history. The separately authorized publication operation creates
the File under the original parent, retaining its stable publication identity.
This ticket's transfer harness never publishes automatically. Pending offline
File operations and their later receipt reconciliation belong to #86.

## Failure handling

Inspect the typed error `code`, `request_id`, `details` and
`recovery_disposition`; never parse message text. Authentication loss requires
reauthorization, invisible resource 404 reveals no metadata, visible denied
action is 403, stale resume state is 409, invalid bytes/size/format are 400,
and excessive request chunks are 413. Storage failure is explicit and never an
empty successful File. Obey 429 and `Retry-After`; discovery is not a throughput
entitlement. A successful queue item remains successful when another fails;
retry only failed items after receipt reconciliation, with their unchanged
transfer identity, operation UUIDs and bytes. The reference queue retains staged
start/chunk operations in memory, skips terminal rejections pending review, and
never automatically replays unknown outcomes. Reload discards this queue; it is
not a durable offline client.

The executable protocol tests use real migrated PostgreSQL and production API
routing. Browser tests pair through the hosted approval flow and exercise the
API reference harness; they do not add offline behavior to the website.

Changelog: additive v1 transfer verification and scoped lookup, exact-byte
download digest/ETag, strict transfer query validation and reference queue;
original-parent collision nondisclosure and non-null request conformance repairs.

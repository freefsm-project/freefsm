# Composed operation admission

Job Status/Field Notes, Subtasks, reviewed Job fields, commercial documents,
Comments, clocks, File publication/mutations and File transport share an
immutable operation UUID registry. Admission, the domain effect, audit and
terminal domain receipt use the committing transaction. A storage/audit rollback
releases admission; an accepted or terminally rejected intent cannot be changed
under its UUID. Domain receipt stores remain independent.

The registry records its deployment identity, user, authenticated client,
operation family and immutable intent. API client identity is the authenticated
mobile session, not an editable device label. Browser publication reconciliation
retains its existing same-user browser identity across sign-in. Matching registry
identity alone never authorizes a new effect: the domain validates its original
receipt and current authority for sensitive representations.

## Files

`POST /api/v1/file-transfers` and each
`PUT /api/v1/file-transfers/{transfer}/chunks?offset=N` require a client-generated
`Operation-ID` header. A transfer resource UUID, start-operation UUID,
chunk-operation UUID and publication-operation UUID are distinct concepts.
Unchanged retry retains the original operation UUID and exact bytes/offset.
Reviewed terminal repair stages a new UUID.

`GET /api/v1/file-transfer-operations/{operation}` returns terminal disposition,
acceptance time and accepted transfer effect identity, without names, parents,
digests or bytes. `GET /api/v1/file-operations/{operation}` supplies safe File
publication/mutation receipts. Sensitive transfer/File reads still require
current original-parent authority. The reference queue reconciles unknown
transport outcomes before replay and preserves the original command.

## Occurrence and schema

Reviewed field commands carry optional nonfuture, microsecond-precision
`occurred_at`, bound into intent and immutable audit independently of server
acceptance. Clock Job activity retains the recorded in/out instants rather than
looking up mutable Time Entry state. Exact request property names, duplicate
rejection and supplied-value types apply at the repaired JSON boundaries.
Identifiable invalid Notes/Status/completion commands receive terminal receipts;
unrecoverable identity or malformed transport does not manufacture a receipt.

## Installed evidence

Migration `082_operation_admission` reserves actual installed receipt identities
without rewriting their intent or inventing historical ordering. Their original
domain receipt validates legacy intent. A historical UUID collision across
domains is reserved as `legacy.identity_conflict`; it is not silently assigned
to a guessed winner. Original receipts remain queryable. Unknown historical
client attribution does not grant a new client replay authority.

All original migration basenames remain installed identities. The deployment
identity and receipts are database state and follow the existing whole-instance
restore contract: a restore cannot reconstruct post-backup effects, and unknown
outcomes never justify blind replay. General batch sync, future mutation domains,
encrypted offline caches and whole-release certification are separate work.

# Composed operation admission

Job Status/Field Notes, Subtasks, reviewed Job fields, commercial documents,
Comments, clocks, File publication/mutations and File transport share an
immutable operation UUID registry. Admission, the domain effect, audit and
terminal domain receipt use the committing transaction. A storage/audit rollback
releases admission; an accepted or terminally rejected intent cannot be changed
under its UUID. Domain receipt stores remain independent.

See [operation receipts and Field Notes recovery](operation-receipts.md) for the
retention floor, safe receipt projection, typed errors, explicit retry/repair and
whole-instance restore caveat. The current stores have no expiry; no replacement
registry or operation namespace is introduced by the receipt refinements.

The registry records its deployment identity, user, authenticated client,
operation family and immutable intent. API client identity is the authenticated
mobile session, not an editable device label. Browser publication reconciliation
retains its existing same-user browser identity across sign-in. Matching registry
identity alone never authorizes a new effect: the domain validates its original
receipt and current authority for sensitive representations.

## Lease issuance and own-session revocation

Clock staging lease issuance and all public API own-session revocations also
use the registry, with independent immutable receipt stores introduced by
`084_security_operation_receipts`. No historical operation identities are
invented for existing leases or revocations.

`POST /time-entries/lease` requires `{}` plus `Operation-ID`; reconcile at
`GET /clock-lease-operations/{operation}`. Its `clock.lease.issue` identity is
distinct from clock-in/out and from the lease resource UUID. Retry returns the
original lease window, never extends it. A forbidden issuance receives a
terminal receipt without lease fields. Audit/storage failures release admission.

`DELETE /session`, `/sessions`, and `/sessions/{sessionID}` require
`Operation-ID` and no body. `session.revocation` binds original client, route
kind and exact target; receipts retain acceptance time and disposition, without
bearers, device labels or non-owned target IDs. Successful HTTP remains 204;
`GET /session-operations/{operation}` returns the immutable receipt.
This closes a preexisting API-05 inventory gap. It does not waive ACC-02:
self logout/revoke-all invalidate their own bearer immediately, including for
receipt lookup. Fresh authentication of the original user permits safe
read-only reconciliation; it never rebinds the original client or authorizes
replay. After security-required state, disablement, expiry, revocation or restore,
authentication/current-authority checks still precede receipt access.

Pairing retains its protocol-specific durable identity and one-use bearer
redemption (ticket76 acceptance criterion2). Automatic successful-request
session renewal is the ACC-02 authentication lifecycle, not a separately staged
user command. Browser account/security actions keep their existing protected
security paths; this inventory concerns the public same-client API mutations.
Read-only transfer verification/recomputation does not create a receipt merely
because its transport is POST. General offline cache/batch synchronization is
downstream under API-03/04 and ticket83, not a waiver of these online identities.

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

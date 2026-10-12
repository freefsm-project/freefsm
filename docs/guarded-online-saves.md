# Guarded online actions

The authenticated router installs a shared online save protocol on existing server
forms. JavaScript form, fetch, HTMX and upload integrations use the same protocol.
It is separate from the public field API/offline protocol.

## Identity and evidence

`save_action_id` (or `X-FreeFSM-Action`) identifies one actor's exact method,
path/query and submitted intent. Admission is persisted **before** dispatch. A
duplicate is never dispatched again, including after restart. A changed intent
using the same identity returns `identity_conflict`. CSRF refresh and multipart
boundary differences do not change intent; uploaded bytes do. An explicit header
can identify an individual file separately from its container form.

Migration `070_guarded_online_actions` adds request admissions and immutable
effect identities. It changes no recorded domain facts, grants, or identifiers.
Submitted values, response bodies, passwords and files are not stored in receipts.
Intent fingerprints use HMAC-SHA-256 with an online-save-specific domain and the
operator's required `Config.SessionSecret`; an application-database copy alone
cannot cheaply verify guesses of submitted passwords or SMTP credentials. Neither
the key nor the payload is journaled. Key rotation causes existing intent
comparisons to conflict (never redispatch), while actor-owned receipt lookup stays
available. Unconfigured test embeddings use a random per-router key, not a shared
default; production configuration requires the operator secret.
The immutable protected-audit writer records effect identities inside the existing
effect/audit transaction. Its failure rolls that transaction back.

For explicitly installed single-command forms, the authorization transaction seals
the primary domain command confirmation before committing. This includes
preferences, company/integration configuration, Item, Customer/master children,
Job/Project/Asset/Estimate/Invoice details, custom fields, vocabulary and workflow
configuration/actions that use that transaction boundary. `services.saveDetails`
already composes Ent builders into the same transaction; it is not wrapped in a
second independent transaction. Native revision, permission, lifecycle, numbering,
Item selection and document evidence checks continue to apply.

Other installed actions receive durable admission and atomic evidence where their
domain emits it. Their successful handler completion can confirm observed effects;
an interrupted, non-reconcilable request remains unknown. Multiple existing domain
transactions are never presented as one atomic HTTP transaction. A legacy handler
without domain effect evidence is `review`, even when it returns a successful HTTP
response. This conservative quarantine is intentional; it cannot become a replay
under a new identity.

## Receipt and review interface

Responses expose `X-FreeFSM-Action`, `X-FreeFSM-Receipt` and
`X-FreeFSM-Disposition`. Authenticated `GET /save-actions/{UUID}` returns:

- `saved`: primary command confirmed in its own transaction, or successful
  handler completion with committed domain evidence;
- `completed`: an explicitly recognized read-only lookup, or a currently
  authorized same-state command, completed without inventing a domain write;
- `unknown`: admitted but request completion cannot be established;
- `review`: handler returned without conclusive domain proof, or partial/error
  completion requires review;
- `rejected`: a recognized single-command form rolled back with no committed
  effect evidence (`retry_safe: true` permits a deliberate corrected command);
- `not_received`: this actor has no admission for the identity **and** the
  originating authentication/history context is proven unchanged. Only that same
  identity may be submitted after checking again.

Pages issue a noncredential opaque history tag in `X-FreeFSM-History`, the
`freefsm-save-history` meta element, and ordinary forms' `save_history` field.
Capture and retain the original tag with each action identity before sending;
send it on receipt lookups and submissions using `X-FreeFSM-History` (native
forms submit the hidden field). It is a separately domain-separated HMAC bound
to the current actor and session, not an authentication credential. Do not replace
it with a new page's tag while reconciling an older action.

After restore, reauthentication, key rotation, or absent origin proof, an absent
receipt is `review` with `history_changed:true` and `retry_safe:false`. Missing
history cannot prove the action never ran. Review current authorized records;
do not replay the original body. Old-tag submissions with absent receipts are
rejected before admission. Existing recorded receipts remain actor-readable
across those context changes. This composes with restore's revocation of all old
sessions; it does not purport to recover discarded post-backup database history.

The response includes nonsensitive `effects` (action/type/ID), `committed_steps`
(effect evidence entries, **not** a count of atomic HTTP requests), `review_url`
and an optional local `location` without query/flash data. Another actor cannot
read the receipt. Receipt access survives original-resource grant loss; following
the review link loads the record/form through current authorization. The installed
HTML review page never shows an old private response snapshot. Dismissal does not
change durable disposition.

## Revision and failure handling

Session, user, and Principal dependency failures return503 without deleting a
session cookie or fabricating authority loss. No command is dispatched when its
initial authentication lookup fails. Actual expiry/revocation/disabled accounts
remain denied. Native failed reads offer a safe Retry link preserving URL filters;
legacy500 query bodies cannot leak SQL as the page. Receipt/input-access lookup
failure is unavailable, never false absence or revoked input access.

Existing-parent forms carry a `save_revision` captured **before** reading the page's
records. A newer token cannot be paired with old displayed values. The committing
authorization transaction compares it under the authority fence and row lock.
Current permission checks take precedence over comparison errors, and a stale
command's effects/audit roll back. Native domain revisions remain authoritative for
their own child/configuration/workflow semantics.

Ordinary controls on rejected forms are reconstructed from a newly authorized form,
retaining only currently editable fields. Password/file inputs and disappeared,
disabled or readonly controls are excluded. Current values appear alongside retained
input. The explicit `Save reviewed correction` submitter advances the displayed
revision; ordinary Save/Enter cannot silently advance it. Configuration's native
revision follows the same rule. Dynamic structured widgets retain their existing
domain renderers and frontend in-place error retention. The shared conflict
renderer additionally restores typed document-line and Job visit/assignment/
subtask JSON into their actual Alpine bootstrap nodes, with current authorized
structured data in the comparison. Transport/security hidden fields are excluded;
JSON script delimiters are escaped. Unknown actions do not
receive a newly executable replay form.

Where a form has both generic and native revisions, the reviewed submitter carries
both as one opaque value. Clients forward it unchanged; ordinary submission cannot
advance either baseline. Known validation/preflight exits explicitly report that
no mutating service was called, rather than quarantining correctable input as a
dispatched uncertainty.

Receipts for ordinary form commands expose `/save-actions/{UUID}/input-access`.
This performs a fresh authorized form read and returns editable field names and
structured widget names, never values. It lets the client remove a lost cost facet
or whole unnamed widget while the intrinsic Own receipt itself remains readable
and unknown. Permission loss does not fabricate resolution of that action.

Migration `071_online_read_only_outcomes` adds explicit no-write completion.
Same-state workflow/master lifecycle commands seal that fact in the owning
transaction after permission and record-lock checks; incidental nested no-ops
cannot confirm a larger request. Coordinate lookup produces an explicit proposal
completion and rechecks Edit authority after provider I/O. HTTP 202 acceptance is
never classified as completed mutation merely from dispatch.

The Job Invoice preparation POST is explicitly read-only and has an equivalent
authorized GET for result recovery. Actual Job-bound Invoice/Estimate creation
continues through the composed domain transaction and seals its own confirmation.

## Backup initial admission

The separate backup engine uses host-local `StateDir/backup-admissions`, outside
the application database and ephemeral backup-job cleanup. Browser create/upload
supplies a pre-generated 256-bit `X-Backup-Action` capability. The engine durably
admits it before any job/stream dispatch and prebinds the native operation ID to it.
Repeated initiation returns409 without reading another upload body or dispatching
again. Passwords and archive/source names are not in these receipts.

`POST /settings/backup/action-status` queries that capability, including during
maintenance or after session invalidation, using the existing CSRF/status-only
boundary. It reports admission/accepted/actual terminal state and whether the live
operation remains available. Available operations continue through the unchanged
native status/download/restore protocol. Terminal facts are fsynced only after the
real engine outcome. Interrupted missing evidence remains unknown after restart;
an unavailable artifact is not recreated by repeating its identity. Restore keeps
its existing operation-capability/destructive-recovery journal, rather than an
application-database receipt that could disappear during replacement.

## Verification boundary

Production-router PostgreSQL tests cover installed parents, identities, duplicate
and changed-intent suppression, concurrent admission, stale preference/Item and
native configuration review, audit rollback, actor/scope loss, restart, and lost
HTTP completion both with and without atomic domain confirmation. Tests also cover
typed structured conflict retention, unknown plus facet loss, read-only/no-op
results, provider-I/O revocation, actual socket-dropped backup create/upload
responses, encrypted download/validation, real restore and session invalidation,
durable restart and real engine terminal-record persistence failure. The shared
persisted Chromium journey covers forms, fetch, HTMX and XHR uploads with lost
responses and permission loss. This does not certify the full requested browser
matrix, every legacy action's permission migration, or exactly-once external SMTP.

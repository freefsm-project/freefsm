# Encrypted offline Field Notes reference

The website remains online-only. The native Node reference is a protocol
exerciser, not a shipped mobile application. It uses the existing Job sync and
Field Notes services and the shared operation admission registry.

## Authorization window

The protected Administrator sets **Offline authorization hours** in the existing
online Settings form (`security.offline.view/manage`, sealed). The default is
24 hours, the maximum is 72, and 0 disables offline access. Invalid values reject
rather than clamp. The update is revision-guarded and audited atomically under
the current-authority fence. Existing nonprotected Roles receive no new grants.

Authenticated `GET /api/v1/offline/policy` returns enabled/hours/maximum_hours.
`POST /api/v1/offline/lease` takes `{}` and a fresh `Operation-ID`, returning
`data` with operation_id, code, accepted_at, id, user_id, epoch, issued_at and
expires_at. Reconcile issuance at
`GET /api/v1/offline-authorization-operations/{operation}`. Replaying issuance
returns the original window; only a new successful online issuance renews it.
The receipt belongs to the original authenticated client; it cannot be used to
rebind another device. Throttle/service failure remains nonterminal. Disabled
policy or insufficient Job View rejects with 403 and a durable rejection.

The general Offline Authorization Lease is distinct from Clock staging leases.
It permits client disclosure/staging, never server mutation authority. The server
still checks current session, independent grants, assignment, active Job and
original Notes revision at commit. An occurrence timestamp grants nothing,
extends nothing, orders nothing and is not a deduplication key.

## Executable and protected storage

`internal/api/v1/reference/offline-notes.cjs` is the executable entry point.
It requires Node and `secret-tool` with a real unlocked desktop Secret Service.
Run `node internal/api/v1/reference/offline-notes.cjs` and provide one JSON
command on stdin. Every command includes `directory`, `store_id` (a unique UUID),
`server` (an HTTPS URL ending exactly in `/api/v1`; loopback HTTP is allowed for
tests), and `action`. The initial `initialize` action binds the protected store
to that server. It refuses a different server on subsequent actions.

| Action | Additional command fields | Result |
|---|---|---|
| `authenticate` | `token` from hosted pairing, supplied through stdin | Online lease; sync required |
| `synchronize` | none | Complete fresh authorized snapshot |
| `read-notes` | `job` from snapshot | Authorized comparison/revision |
| `stage` | `job`, `text` | New UUID, pending |
| `view` | none | Currently accessible cache and pending dispositions |
| `submit` / `reconcile` | `operation_id` | Explicit submission / receipt-only lookup |
| `acknowledge-uncertain` | `operation_id` | Explicit outcome review after restore receipt miss |
| `repair` | `operation_id`, `text`, `notes_revision` explicitly reviewed | New UUID on the original target |
| `discard` | `operation_id` | Local removal |
| `logout` | none | Owned key and ciphertext destruction |

Do not place an authentication command in a shell history, plaintext input file,
or log. Pass the pairing result via a protected pipe/in-memory caller. `view` and
`read-notes` deliberately return authorized sensitive content to the interactive
caller; do not redirect that output to unprotected logs. Other commands return
only safe state/identity metadata. The error path reports stable safe codes.

Use a private, nonsymlink directory (mode0700) and unique store identity. No
key material is accepted from command-line arguments or saved beside the cache.
The cache is one AES-256-GCM authenticated envelope containing the bearer,
authorized snapshots, lease, Notes revisions and pending intents. Each save has
a fresh random nonce; version/store identity are authenticated. A temporary
mode0600 ciphertext is fsynced, atomically renamed and the directory fsynced.
Key loss, wrong key, corruption or unavailable protection fails closed. There is
no plaintext fallback or automatic replacement key for existing ciphertext.

Secret Service protects the key under the user's platform login/keyring. This
does not defend against an already compromised account or claim hardware-backed
protection. The process necessarily handles plaintext in memory; JavaScript
garbage collection is not a secure-memory erasure guarantee. Do not log transport
bodies/credentials. Secret subprocess output is never propagated into errors.

Actions are serialized with an exclusive writer marker. Concurrent writers fail
closed. If a process crashes while holding the marker, verify that no writer is
running before handling `.writer`; there is no timed lock takeover. In particular,
`protected_store_locked` leaves that durable marker when an authority-loss write
fails, so restarting cannot expose the older cached authority. Do not simply
remove the marker and view old content: obtain fresh authentication/current sync
in a separate store and recover only authorized pending intent through explicit
review, or destroy the old key/data. This reference deliberately fails closed
rather than offering automatic damaged-store repair.
An interrupted cache write retains either the complete old or complete new
ciphertext. A submission is durably marked unknown **before** transport.

## Explicit state machine

Authenticate online, synchronize a complete assigned-Job snapshot, read current
Notes, then stage reviewed text. Staging captures the original opaque target,
revision, UUID and occurrence instant. It visibly returns **pending**, not
accepted. Exact `now >= expires_at` locks view and staging; clock rollback below
the persisted high-water instant also locks. Occurrence supplied by an intent
cannot extend the window. Restart reads the protected store; it never replays.

Submission and retry are explicit. Every unknown outcome first queries its
original safe receipt. A confirmed ordinary miss permits an unchanged same-ID
retry; it does not prove failure.
The stored server retry delay (five seconds when unspecified) also gates an
explicit retry after throttle/dependency failure; checking a receipt does not
erase that delay. It never changes the operation ID or original intent.

Receipt lookup does not require Job access or a live offline lease, but does
require current server authentication. Sensitive
reads still require current authority. Terminal conflict is **Needs attention**:
read authorized current Notes and compare, then explicitly repair with the
reviewed revision and a new UUID, or discard. Target/revision rewriting,
automatic merge, force overwrite and automatic replay are unavailable.

## Ordinary purge versus restore

401/403 ordinary authorization failure purges credentials/cache/sensitive intent
on contact. A typed scope resnapshot purges rather than retaining a speculative
comparison. A successful fresh snapshot removes pending content for absent
targets. Offline disablement is checked on online contact. No remote erasure of
a disconnected device or exported copy is promised. Logout deletes the owned
Secret Service item and ciphertext; an unavailable key service reports deletion
failure explicitly even though ciphertext is removed.

`instance_restored` + `locked_review` instead removes credentials and operational
cache while retaining recoverable intent encrypted and locked. Fresh restored-
account authentication alone does not unlock it: complete current-authority sync
is also required. Inaccessible targets are purged. Authorized old intent becomes
**restore_uncertain**, never pending/retryable. Query original receipts. A found
receipt is authoritative for retained history; a missing post-backup receipt is
**uncertainty**, not failure. Explicit external outcome reconciliation and review
must precede any new-ID repair. User identity changes purge old sensitive intent.
Even a locally retained old acceptance receipt is marked uncertain until queried
against restored history; it is not proof that a post-backup effect survived.
No automatic action occurs during recovery, including after interruption.

## Compatibility and evidence

Migration086 only adds offline policy and immutable issuance receipts. Installed
identifiers, domain facts and grants are preserved. Notes routes accept stable
sync UUID identities as well as existing decimal-string identities; both resolve
to the original installed Job and its unchanged revision/receipt service.
The local OpenAPI3.1 document describes the additive contract. Owning tests cover
real Secret Service lifecycle, exact expiry/restart/wrong-key failure, ordinary
purge and restore separately, plus production HTTP/DB acceptance and denial.
Broader batches and financial restore recovery remain separate slices.

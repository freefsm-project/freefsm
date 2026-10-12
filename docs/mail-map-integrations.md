# Mail and map integrations

## Authority and routes

- `/settings/mail`: `configuration.mail.view` and independent `.manage` (U).
  Sender, SMTP transport, default copies, and plain/HTML templates live here.
- `/settings/test-email`: independent `configuration.mail.test` (U), with its
  catalog-required mail View. It requires one explicitly entered recipient and
  uses saved transport, never unsaved fields from the test request.
- `/settings/maps`: `configuration.maps.view` and independent `.manage` (U).
- `/settings/open-tracking`: sealed `security.open_tracking.view/manage` (S).
  Existing values are retained; fresh installations default to off.
- `/locations/{id}/coordinates`: Location View (A); accepting or looking up a
  proposal requires Location Edit (A). A map configuration grant cannot edit a
  Location. The coordinate editor is linked from Customer Location rows/cards.

Configuration is browser-only; the public field API does not acquire these
routes. Role labels do not authorize them. Settings saves lock current authority
and the settings row, compare the reviewed revision, and commit the effect with
shared immutable audit. Concurrent saves from the same review have one winner.
The legacy combined settings form no longer writes integration settings.

## Operator keys and credentials

Set `FREEFSM_CREDENTIAL_KEYS` to a comma-separated list of base64-encoded **32-byte
random keys**, active write key first. Generate a key with `openssl rand -base64
32`. Keep keys in the service environment or restricted configuration file
loaded by `-config`; existing environment variables take precedence over file
values. Restrict that file to the service account/operator. Keys are independent
of session and bootstrap secrets. Restart the service after operator changes.

SMTP passwords use standard AES-256-GCM with a fresh random nonce. The format is
versioned (`aesgcm:v1:`). Settings return only password presence and review status;
password fields are always empty. Blank retains/re-encrypts the saved password;
the explicit Remove checkbox clears it. Validation, audit and provider errors
never include passwords, ciphertext, AUTH payloads, or raw SMTP diagnostic text.

For lossless rotation, install `new,old` keys, restart, and save Mail settings
with the password blank. This decrypts with an available key and re-encrypts with
the active key. Verify the configuration before retiring the old **live** key.
Retain old keys separately for historical archives. Product users cannot recover
the plaintext through the settings interface.

Startup rejects malformed configured keys or recognized encrypted credentials
whose keys are unavailable. The read-only startup preflight and settings views
identify legacy plaintext/unknown formats for explicit review without rewriting
or automatically sending mail. Replace the credential through Mail settings (or
explicitly remove it). Original stored values remain intact until that action.

Authenticated encrypted backup manifests retain the encrypted credential records
needed to verify destination key availability. Restore validates keys before
destruction, rechecks before replacement, and validates actual restored records
before committing. Missing keys fail with operator remediation; they never
produce a successful recovery with unusable credentials. Keys themselves are
external to the archive and must be backed up separately.

## Transport, snapshots, and restore

SMTP success means **accepted by transport**, never inbox delivery. Failed test
submission is not confirmation of acceptance; an ambiguous post-handoff failure
requires provider review before another send. Raw provider errors are redacted
while SMTP status-code classification remains available to the worker.

New outbox records freeze sender along with recipient, subject, plain/HTML body,
PDF bytes/name, and stable Message-ID. The worker reads current transport and
decrypts credentials at send time; configuration changes do not rewrite frozen
content or attempt counters. HTML defaults are editable during compose, and
blank HTML uses the edited plain text. SMTP evidence stops at Accepted; only
authenticated provider evidence can establish later delivery/bounce states.

Historical snapshots without a recorded sender retain their IDs, contents,
attempts and evidence. Migration deliberately leaves the unknown sender NULL.
The worker does not invent a sender or claim these records. Preflight reports
them for review; a new explicitly reviewed send creates a new Delivery rather
than rewriting history. This is an upgrade exception, not a pause/resume control.

The external restore email-disable marker remains authoritative across restart
for tests, invitations/recovery mail and workers. Settings changes cannot clear
it. A current protected Administrator must explicitly re-enable mail. The sealed
decision and immutable audit commit before removing the marker; failure before
publication leaves email disabled. An audit intent records authorization, not a
claim that the external filesystem or SMTP provider completed an action.

## Maps and geocoding

Supported map integration remains tile URL templates and Nominatim-compatible
HTTP(S) geocoding. Saved URLs take precedence; blank uses the operator defaults,
if configured. Credential-bearing/query-token URLs are rejected for new saves;
legacy ambiguous URLs remain stored but are withheld from views and require
explicit replacement. No new provider account or provider evidence is invented.

Viewing a schedule never geocodes or writes coordinates. Lookup reads the current
provider and returns a proposal associated with the reviewed Location revision.
The response rechecks current access/address, and accepting coordinates rechecks
the revision, authority and active parent under transaction locks. A late
response or stale acceptance cannot overwrite a newer address. Provider errors,
empty results and invalid coordinates leave saved data intact; manual address
entry, manual coordinate entry, list, calendar and dispatch stay independent.

## Verification scope

Ticket #26 tests use migrated PostgreSQL, authenticated production browser HTTP,
field-API denial, actual SMTP sockets, backup/restore APIs, and persisted Chromium
journeys. They cover independent grants, write-only encryption and rotation,
missing-key rejection, audit rollback, stale/concurrent saves, waiting-request
revocation, explicit geocode acceptance, provider failure, frozen SMTP content,
truthful acceptance, restart-persistent restore gating and pre-v1 preservation.

The broader Delivery lifecycle (per-recipient fanout, extra attachment formats,
retry budgets, cancellation and complete document permission migration) remains
with its owning Delivery tickets; this change preserves those existing records
and does not claim to complete all of COM-05. Native OS/reboot and large-archive
scale acceptance are not established by these focused integration checks.

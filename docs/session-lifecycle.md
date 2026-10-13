# Browser and mobile sessions (#23)

## User and Administrator journeys

The account menu's **Sessions** link opens `/sessions`. It lists only the acting
user's sessions, including the current session, device label, recorded issue and
use times, expiry and retained revocation evidence. Revoke one session or all
sessions using the CSRF-protected forms. Current-session revocation returns to
login. Logout remains available during mandatory password replacement.

A protected Administrator can follow **Inspect sessions** from the existing
user detail page to `/users/{userID}/sessions`, and revoke one or all of that
user's sessions. An ordinary Role, including an empty, renamed or replacement
Role, always retains its intrinsic own-session access. This does not grant
directory, other-user or security-configuration access. Session lists never
contain bearer tokens or their hashes.

Browser validity is exactly 604800 elapsed seconds from issue, without renewal.
Mobile validity is exactly 2592000 elapsed seconds from the most recent successful
online authorization. Elapsed-hour SQL intervals deliberately avoid daylight
saving transitions changing those durations. Authenticating a token alone does
not renew it; denied/not-found/failed API requests, offline work and nonexistent
refresh endpoints cannot renew it. Successful API responses renew under the
same authority fence as revocation, with current identity and expiry checks.

Revocation records are retained. Email, Administrator and host-local resets;
disablement; required credential change; and explicit revocation invalidate the
affected browser and mobile sessions. Self-password change retains only its
acting browser. The existing temporary-password and recovery flows are retained.
An expired browser action says **Session expired—sign in again**. Login does not
resubmit that action. Session-management forms use native credential navigation
so deliberate self-revocation is not mistaken for an interrupted product save.

## API and restore protocol

The existing account login and current-session logout routes remain available.
Additional bearer-authenticated own-scoped routes are:

| Method | Relative `/api/v1` path | Effect |
|---|---|---|
| GET | `/sessions` | Secret-free own-session summaries |
| DELETE | `/sessions/{sessionID}` | Revoke one owned session |
| DELETE | `/sessions` | Revoke all owned sessions |

Other-user session administration is browser-only. Revocation is terminal;
repeated single-session revocation does not duplicate audit. Repeating logout
with an already-revoked bearer returns the ordinary 401/purge disposition.

Every API response includes `X-FreeFSM-Session-Epoch`. Clients persist the epoch
received at login alongside pending intent and send it on subsequent requests.
The epoch is a nonsensitive recovery-generation identifier, not an authorization
credential. Dependency failures return unavailable responses, not instructions
to discard credentials.

* Ordinary invalid/revoked/disabled credentials return an error with
  `recovery_disposition: "purge"`. Credential-change-required responses also
  carry that disposition. Purge cached data, keys and ordinary pending work on
  that contact.
* A different supplied epoch returns HTTP 401, `code: "instance_restored"`, and
  `recovery_disposition: "locked_review"`, before old credentials are accepted.
  Purge ordinary cached representations, retain recoverable pending intent in
  locked storage, and require explicit fresh authentication without the old
  epoch. Synchronize current authority before presenting any pending work.
  Remove inaccessible intent/content; authorized uncertain outcomes require
  explicit reconciliation. Never replay automatically.
* Clients lacking a previously recorded epoch cannot distinguish their old
  token's cause of invalidity. They receive the ordinary safe purge response.
  Thus a conforming recoverable-pending client must retain the epoch, including
  across application restart. A client-supplied mismatch grants no data access.

The encrypted restore protection transaction rotates the restored database's
epoch and revokes restored sessions, while destination tokens also become
invalid. A rollback restores the previous generation; a completed restore keeps
the new epoch across restart. Existing restore maintenance and all-path email
disablement remain in the restore subsystem.

`docs/api/session-reference.cjs` is an executable reference state machine, not
a production offline client. `TestSessionReferenceHarnessHTTP` drives it through
the production API for ordinary purge and fresh-auth/current-authority locked
review, without a replay callback. `TestBackupHTTPRoundTrip` additionally proves
the recovery response through real encrypted HTTP restore for both destination
and restored-source mobile tokens. A real platform client still owns encrypted
storage, key destruction and its operation-specific synchronization protocol.

## Non-destructive upgrade and explicit review

`session_upgrade_inventory` is immutable and contains no bearer material. It
preserves the recorded session ID, user, type, issue/use/expiry/revocation times
and upgrade disposition. Before migration 052 runs on an older installation,
the migration runner captures the genuinely absent type and last-use fields as
NULL. It does not retrospectively label them as recorded browser activity.
For installations where 052 already ran, the inventory preserves the existing
recorded fields and flags recognizable pre-052 issue-time backfills for review.

Unknown classification/timing, inconsistent timing or an overlong deadline is
`review-required`. Such an old session cannot authenticate or commit an action;
its recorded values and original revocation facts are preserved. A fresh account
login follows the normal password/mandatory-change flow. From the fresh own
session or a protected Administrator session, inspect the recorded evidence in
**Sessions**, explicitly revoke the quarantined session, and record that
reauthentication disposition in the immutable revocation audit. Never update
inventory facts, fabricate activity, extend an old deadline or reuse its token.
No Role grants, account IDs or relationships are changed by this migration.

## Verification commands

The ticket evidence file records concrete results and disposable database URLs.
The principal reproducible checks are:

```sh
make templ
make lint
FREEFSM_TEST_DATABASE_URL="$DISPOSABLE_DATABASE_URL" \
FREEFSM_BACKUP_TEST_ADMIN_URL="$DISPOSABLE_CLUSTER_ADMIN_URL" make test
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
NODE_PATH=/path/to/test/node_modules \
FREEFSM_TEST_DATABASE_URL="$DISPOSABLE_DATABASE_URL" \
go test -race ./internal/handlers -run 'TestSessionsPersistedBrowser|TestRecoveryPersistedChromium|TestOnboardingPersistedBrowser' -count=1
```

Use disk-backed `TMPDIR`, `GOTMPDIR` and `BUILD_DIR` when `/tmp` is quota-limited.

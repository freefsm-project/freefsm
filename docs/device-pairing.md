# Browser-approved devices

The normative executable boundary for pairing and own-session transport is the
locally served **`/api/v1/openapi.json` (OpenAPI 3.1)**. The embedded source is
`internal/api/v1/reference/openapi.json`. `/api/v1/docs` contains the pairing,
session, recovery, error, compatibility and interrupted-action guide.
`/api/v1/reference` is an interactive exerciser, not a production client or an
offline website. It needs no CDN or external documentation service.

## Protocol

1. Start with a nonblank device label (maximum 100 characters) and a new UUID
   operation ID. Keep the returned device secret private; display the separate
   user code and ten-minute absolute expiry.
2. The user signs into the hosted browser, completes any mandatory password
   replacement, enters the code at `/pair` and reviews the displayed label.
   Explicit approval is bound to that browser session and reviewed label.
3. Redeem with the device secret and another UUID operation ID. Return the opaque
   bearer only after atomic session issuance, protocol consumption and immutable
   audit commit. Codes, review secrets and bearers are stored as hashes.

The approving account, browser session, unchanged credential and expiry must
remain valid at redemption. No Permission is added; this uses intrinsic own
authority and the shared account/session transaction fence 190019. Concurrent
redemption yields at most one session. Cancelling an unconsumed pairing prevents
issuance. Cancellation also carries a UUID operation ID; same-ID cancellation
replay returns no additional effect. Interrupted review/approval never automatically creates a session.
Protocol-specific durable replay protection is independent of domain receipts.

The bearer is not replay-recoverable. After a lost successful response, inspect
and revoke the labelled session through `/sessions`, then explicitly start a new
pairing. Do not infer that an uncertain response means no effect. No automatic
replay follows password replacement, restoration or reauthentication.

The existing session service retains the fixed seven-day browser login and
30-day mobile inactivity rules, own list/revoke/current logout/revoke-all,
disablement, force-change, password-reset/change and restore behavior. See
[session lifecycle](session-lifecycle.md). Discovery, docs and pairing disclose
no deployment/session epoch. Effective grants and limits require authentication.

## Operator policy

| Setting | Default per sliding minute |
|---|---:|
| `FREEFSM_AUTH_PER_LOGIN` | 5 per trimmed lowercase login |
| `FREEFSM_AUTH_PER_IP` | 20 per IP |
| `FREEFSM_API_PER_USER` | 300 shared across a user's bearer devices |
| `FREEFSM_API_SYNC_PER_CLIENT` | 12 per bearer client at the `/sync` boundary |

These must be positive integers. Hosted authentication uses login and IP budgets;
password-free pairing uses IP budgets. Synchronization's transport boundary is
budgeted here; domain synchronization behavior belongs to its owning ticket.
Limits are not throughput promises. Counters are bounded, process-local,
sliding-minute windows and reset on restart. Multi-process deployments need a
shared upstream abuse budget. Unknown authentication identities consume the
same budgets as known ones; no existence response is introduced.

`FREEFSM_API_CORS_ORIGINS` defaults empty/off. When configured, supply comma-separated
**exact HTTPS origins**, without paths, credentials, wildcards or trailing slash.
Preflight permits the documented bearer headers, never credentialed cookies.
Invalid origins fail startup. HTTP 429 is typed JSON with request ID,
`retry_after_seconds` and `Retry-After`; clients must still handle dependency
failure and current-authority denial. Same-origin browser CSRF/cookies remain in
the hosted browser, not the bearer API.

## Compatibility and verification

Pre-v1 password login is retired with `410 pairing_required`. Published v1 changes
are additive; breaking behavior needs a new major. Clients ignore unknown response
fields, safely withhold action for unknown enum/error values, and never parse
message prose. The server rejects unknown request fields and malformed types.

With an isolated migrated PostgreSQL target:

```sh
FREEFSM_TEST_DATABASE_URL=postgres://... go test -race ./internal/handlers \
  -run 'TestPairing|TestHostedAuthentication|TestCredentialLoginRaces|TestAPILoginThen'
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium NODE_PATH=/path/to/playwright/node_modules \
  FREEFSM_TEST_DATABASE_URL=postgres://... go test ./internal/handlers -run TestPairingPersistedBrowser -count=1
make lint
FREEFSM_TEST_DATABASE_URL=postgres://... make test
```

`TestPairingOpenAPIConformanceHTTP` fetches the actual hosted document, compiles
its JSON Schema 2020-12 request/response schemas and validates production HTTP
statuses, bearer requirements, typed errors and replay behavior. Browser coverage
drives the real reference harness and approval UI, checks CSRF, persisted labelled
issuance and revocation, and verifies no bearer in DOM, URLs, logs or browser
storage. Upgrade coverage compares installed users, Roles, grants, sessions,
Customer data and audit before/after migration. Only the new protocol table and
purpose-specific anonymous audit attribution are introduced; installed grants
and historical migration files are preserved.

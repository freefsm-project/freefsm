# Ticket #76 integration acceptance

## Scope and ancestry

Accepted ticket **#76 — Pair devices through browser approval**, scoped to its
twelve owning acceptance criteria. Source tip
`505f523ef7934aa2a467c1b0a135ba8f007a3085` was clean and based on accepted integration
`23da4af7a3f3242d7a30188723449e048c2b2da2`. Integration started clean at
`a72e1c3dca64e64384cc250d2b059bb8309ff53d`, which additionally includes #103.
Only the assigned #76 tip was merged. Both branches' audit behavior is retained.

## Composition repairs

1. **Issued-session audit identity.** #103 resolves API audit attribution against
   a mobile session. Pairing had supplied the approving browser's hash, causing
   combined redemption to roll back with 503. Issuance now supplies the new mobile
   hash to the audit writer while retaining the approving browser actor for final
   authority revalidation. The regression checks bearer usability, one issuance,
   device/session/operation attribution, captured actor/Role names and immutable
   identities, and absence of bearer/hash material in audit detail.
2. **Readable admission failures.** Exact-origin response policy is shared with
   outer maintenance/connection-refresh errors. Configured clients can read typed
   503 and Retry-After; untrusted origins receive no CORS grant. Browser admission
   locks and CSRF remain intact.
3. **Executable conformance.** The served-document checker now resolves actual
   response references, derives request schemas from operations, checks bearer
   requirements, JSON content type and declared headers, and asserts expected
   statuses. Independent live bearers prove successful revoke-one, logout and
   revoke-all, followed by authorization denial.

These changes preserve the current-authority fence and atomic audit boundary.
They add no Permission, legacy mapping, refresh protocol or website offline mode.

## Criterion matrix

| # | Owning criterion | Acceptance evidence |
|---|---|---|
| 1 | Short-lived password-free labelled once-only bearer | Browser approval, ten-minute deadline/commit-expiry, concurrent redemption and Chromium pairing tests pass; retired password route returns 410. |
| 2 | Cancellation, expiry, wrong approval, replay and interruption | Concurrent redemption/cancellation, browser-bound review, terminal operation state, router-reconstruction replay and audit-failure HTTP tests pass. |
| 3 | Canonical mobile/browser lifetimes and forced-change rules | Existing fixed seven-day browser and thirty-day successful-online-authorization mobile session tests pass; pairing browser-expiry/force cases pass. No refresh flow introduced. |
| 4 | Own sessions and lifecycle invalidation | Real paired-bearer list/revoke/logout/all, reset/disable/force races, restore invalidation and persisted session/recovery journeys pass. |
| 5 | Minimal discovery; private grants/limits/sync | Public boundary and epoch-nondisclosure tests pass. Capabilities and synchronization budget boundary require authentication. |
| 6 | Configurable 5/login, 20/IP, 300/user, 12/client limits | Default/configured limits, normalized/query-backed login, shared browser/API IP budget, mounted sync and typed 429 tests pass. |
| 7 | Default-off exact HTTPS CORS and preserved browser protections | Configuration rejection, bearer/preflight policy, outer-error regression and persisted CSRF/recovery/onboarding checks pass. |
| 8 | Local OpenAPI 3.1, guides/errors/examples and safe reference | Served-document conformance and persisted interactive harness pass; bearer remains in closure memory with no automatic replay or browser storage. |
| 9 | Independent/current authority; atomic effects and immutable audit | Intrinsic own-session authority, empty-role pairing, credential races, delayed commit expiry and all pairing audit rollback cases pass. #103 audit tests and captured issuance evidence pass in the combined tree. |
| 10 | Non-destructive upgrade and installed data preservation | Pairing upgrade compares users/Roles/grants/sessions/Customers/audit before/after/repeat migration and pairs an installed empty-role user. Both 074 migrations are retained; migration tracking uses complete filenames. |
| 11 | Real DB/API/browser/failure/concurrency; mandatory quality | Fresh PostgreSQL 18.6, explicit DB/backup variables, full lint/race suite and applicable Chromium checks pass; skips are qualified below. |
| 12 | API/reference scope and own-document conformance | Publication covers pairing/identity/capabilities/own sessions; website remains online-only. Strengthened schema/status/security/session-success coverage passes. |

## Reproducible verification

Durable evidence root:
`/home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-76`.
`run.sh`, `verify.sh` and `go-test-timeout.sh` retain the exact commands. Every
heavy invocation acquires the shared `heavy-tests.lock`, with:

```sh
export GOMAXPROCS=2 GOFLAGS=-p=2
export TMPDIR=/home/adam/repos/freefsm/.agent-state/v1-spec-18/m76
export GOTMPDIR=/home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-76/go
export FREEFSM_TEST_DATABASE_URL='postgres://freefsm@127.0.0.1:55876/merge76?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL="$FREEFSM_TEST_DATABASE_URL"
make lint
FREEFSM_TEST_DATABASE_URL="$FREEFSM_TEST_DATABASE_URL" \
  make test GO="bash /home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-76/go-test-timeout.sh"
```

The wrapper adds only `-timeout=45m`; the Makefile still runs
`go test -v -race ./...`. The cluster was newly initialized under this evidence
root, never copied from an archive or pointed at a host database. Its test-only
backup extension is isolated under `pg-share/extension`.

| Check | Actual result |
|---|---|
| Red reproduction | `logs/red-runtime.log`: redemption 503 and missing configured-origin error headers; exit 1. |
| Repaired regressions | `logs/green-final.log`: exit 0; handlers 3.375s. |
| Generated templates | `make templ`: pass, zero updates. |
| Selected uncached HTTP/DB/concurrency/upgrade/browser race run | `logs/acceptance.log`: exit 0; handlers 118.696s; applicable pairing, session, recovery, onboarding and protected-audit Chromium journeys pass. |
| Full `make lint` | `logs/quality.log`: `LINT_EXIT=0`. |
| Full explicit DB + backup `make test` | `logs/quality.log`: `TEST_EXIT=0`, `quality_EXIT=0`; backup 672.387s, handlers 799.250s, actual uncached execution for both. |
| Whole runner | `verify-exit-status`: 0; quality completed 2026-10-13 06:11:22 -05:00. |
| Screenshots | `pairing-browser.png`, `audit-browser.png`. |
| Whitespace | Working and staged `git diff --check`: pass. |

The full suite leaves optional browser switches off. Applicable browser journeys
above were separately enabled with `BROWSER_TESTS=1`, durable `NODE_PATH` and
`BROWSER_EXECUTABLE=/usr/bin/chromium-browser`. Other domain/installation/browser
and large nginx-scale opt-ins are not claimed passed. The PostgreSQL <16 negative
test skips on PostgreSQL 18.6. Standalone subprocess helpers skip outside their
parent harness; their parent checks run. Exact skip lines remain in the logs.

Earlier test-authoring errors (one Go identifier and one SQL column name), an
initial port bind conflict, and a lock-wait invocation terminated by tool timeout
are retained in the durable record; none is represented as passing evidence.

## Boundaries

No remaining #76 blocker was identified after combined verification. Field-domain
OpenAPI expansion, generic mutation receipts, synchronization/offline clients,
resumable Files and final whole-spec/release/browser certification remain with
their owning tickets. Synchronization here proves the authenticated request-limit
boundary, not an implemented synchronization service. No issues were closed and
nothing was pushed.

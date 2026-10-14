# Authorized Job synchronization (v1)

`GET /api/v1/sync/jobs` is the paired-user, read-only Job-context protocol.
The locally served [OpenAPI 3.1 document](/api/v1/openapi.json),
[protocol guide](/api/v1/docs), and [interactive exerciser](/api/v1/reference)
describe the wire contract. Pair through hosted browser approval first; see
[device pairing](device-pairing.md). The website remains online-only.

## Scope and filters

Send `Authorization: Bearer <paired token>` without browser cookies. Remember
the authenticated `X-FreeFSM-Session-Epoch` and return it on subsequent requests.
Never put bearer credentials in URLs, logs, DOM, or unprotected storage.

| Query parameter | Contract |
|---|---|
| `scope` | `assigned` (default), or `all` with independent Job View A. |
| `page_size` | Integer 1–200, default 50. |
| `page` | Opaque next-page token from the previous successful page. |
| `cursor` | Opaque completion cursor from a fully applied snapshot/delta. |

Each parameter occurs at most once and must be nonempty. `page` and `cursor`
are mutually exclusive. Unknown parameters, repeated parameters, unknown scope,
and invalid sizes return HTTP 400 `invalid_request`. There is no query expansion,
arbitrary sorting, relationship traversal, date-range filter, or field selection.
Keep scope and page size identical throughout a chain. Treat tokens as opaque;
changing the chain's filters or using another device's token requires resnapshot.

`GET /api/v1/capabilities` publishes current grants, `sync` availability/scopes,
endpoint, supported filters, page bounds, and 30-day continuity. Its `limits`
include the configurable default **12 synchronization requests/minute/client**
(and 300 requests/minute/user). Each page and replay consumes the sync budget.
Capabilities never authorize an action; every page checks current authority.

## Closed representations

Each `records` entry is a **complete replacement** of one Job: opaque `id`,
`title`, `description`, `field_notes`, `status` (name/category), nullable start/end
instants, due date and arrival-window instants, IANA `timezone`, optional
`customer`, and `masters`. Absent context facets remove previously cached facets.

All context requires current explicit Job assignment, including for users with
All Records grants, and independent View for each facet. Exact nested schemas
are closed in the served OpenAPI:

* Customer: identity, display/person/organization names, Kind, phone/email,
  Service Notes.
* Explicit Contact(s): identity, name, role/title, phone/email, Service Notes.
* Location: identity, title, service address, coordinates, Access/Service Notes.
* Operational Asset(s): identity, Number/name/class/type/status/serial,
  manufacturer/model, installed/warranty dates, current Location identity/title,
  Service Notes. Singular compatibility aliases and plural explicit links use
  the same closed DTOs.
* Active authorized Project: name/description/Status/progress, planned dates,
  linked Location title and Service Notes. An archived Project without independent
  archived detail authority is only `project_reference: {name, archived: true}`;
  it has no identity or navigation target.

There are no billing fields, tax/financial/Credit data, internal notes, master
custom fields, sibling work, recursive collaboration, Items, unrestricted master
history, Service Entries or Lease coverage in this initial sync contract. Later
slices own their independently authorized snapshots/deltas/revisions/capabilities.
This endpoint adds no mutation, operation receipt, File transfer, or offline lease.

Job and nested context identities are opaque strings, stable across snapshots;
installed IDs and relationships are preserved. IDs are never reused. Instants
use RFC3339 offsets; dates use ISO `YYYY-MM-DD`; timezones use IANA names.
Status categories use `job:*`; an empty category on installed data means no
recorded Status, not an inferred New status. Customer Kind is `Individual` or
`Organization`; a legacy empty Kind remains unspecified. No monetary or occurrence/acceptance fields are fabricated;
later monetary contracts require exact two-decimal USD strings, and mutation
contracts must distinguish occurrence and acceptance time.

## Snapshot, deltas, and atomic application

Begin with no token, for example `GET /api/v1/sync/jobs?page_size=50&scope=assigned`.

```json
{"data":{"records":[],"removed":[],"next_page":null,
  "completion_cursor":"opaque-completion-token",
  "watermark":"2026-10-14T12:00:00Z","complete":true,"purge":"none"}}
```

The initial read materializes a coherent authorized snapshot at `watermark`.
Pages are stable and deterministic. A nonfinal page has `complete: false`,
`next_page` and a null `completion_cursor`. The final page has `complete: true`,
a null `next_page` and a completion cursor. Empty snapshots/deltas still complete.
The completed baseline is the original materialization; a subsequent cursor read
compares a fresh materialization, catching changes committed during pagination.
Deltas represent current state differences, **not an event or audit history**.

1. Stage a fresh snapshot separately from the prior selected-scope cache. Do not
   merge an initial snapshot into stale records. Mark synchronization incomplete.
2. Validate the whole response. Ignore unknown response fields; unfamiliar enums
   require safe display/review, never an inferred action or success.
3. Purge every `removed` identity from both committed and staged state, including
   records delivered on earlier snapshot pages. Removal discloses identity only.
4. Upsert each complete Job replacement by opaque identity; never append a copy
   or infer new history from replay. Remove omitted old facets.
5. Atomically commit the page application and its continuation metadata. Promote
   the completion cursor only after **all pages and local application succeed**.
   Durable clients must make data and cursor one protected-store transaction.
6. Request the next page explicitly; after completion, use `cursor` for deltas.
   Retry a failed page with the same request/token. Never skip an unapplied page.

A network failure, malformed JSON, server/storage failure, or local application
failure leaves the process incomplete and retryable. Preserve the last completed
cursor, not a response's uncommitted cursor. Purges for lost authority must not be
rolled back by a later local application failure. Token replay returns the same
materialized successor subject to current-authority withholding/removals.

## Errors and recovery

Errors carry `code`, `message`, `request_id`, `details`, `recovery_disposition`
and optional retry timing. Branch on status/code/disposition, not message text.

| Status / code | Client action |
|---|---|
| 400 `invalid_request` | Correct the documented query; do not promote a cursor. |
| 401 ordinary authorization loss | Purge credentials and cached context on contact; pair again. |
| 401 `instance_restored` | Lock recoverable pending intent for fresh authentication/current-authority sync and explicit review; no blind replay. Inaccessible context is still purged. |
| 403 `forbidden` | Purge Job/context; current grants no longer permit this scope. |
| 409 `resnapshot_required` | `recovery_disposition: purge_and_resnapshot`, `details: {purge: all}`. Discard all Job/context caches and tokens; start without a token. |
| 429 `rate_limit_exceeded` | Observe `Retry-After` and `retry_after_seconds`; explicitly retry without advancing continuity. |
| 503 `unavailable` | Retry the same request after indicated delay; no completion acknowledged. |

Continuity lasts at least 30 days. Expired/unknown/cross-client tokens, mismatched
filters, changed grants, and unsafe relationship changes require resnapshot.
Assignment/deletion loss can instead withhold inaccessible records and return
ID-only removals on any page, including removal of already delivered identities.
Denials purge even if the error body cannot be parsed. No remote-erasure promise
is made for disconnected clients or exported copies.

## Reference exerciser and executable conformance

The reference page pairs through the real browser, then offers snapshot, one-page
continuation/retry, delta, and replay controls. It uses closure-memory maps and
atomic pointer replacement to demonstrate cache/cursor ordering. A labelled
local-application failure switch demonstrates retry without false completion.
It has no persistent storage, encrypted-cache claim, service worker, offline
authorization lease, background sync, automatic retries, or pending mutations.
Reload/logout discards data. Production clients require platform-protected
encrypted credential/cache storage and owning offline lease/recovery protocols.

`TestJobSyncReferenceOpenAPI` validates real paired production HTTP against the
served OpenAPI 3.1 JSON Schema, query parameters, success/error status and auth,
exact nested DTOs, resnapshot and retry timing. The browser test reads persisted
Jobs and covers concurrent edit, previous-page scope removal, local/transport/JSON
failure, retry/replay, additive fields/unknown enums, grant and session loss.
Run with the project's disposable PostgreSQL URL and `BROWSER_TESTS=1`.

### Changelog

v1.1 adds read-only Job synchronization and authenticated sync discovery to the
published pairing/own-session protocol. Existing pairing controls and schemas
remain compatible. Published v1 evolution is additive; breaking contracts need
a new major. Later domain slices publish their own contracts and conformance.

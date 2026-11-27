# #106 implementation contract

2026-11-26. Base `623a8655fe44502db4078da79653e5bb3de67c1d` is user-approved. Scope is combined search and the ten main record collections; reports, pickers, and related sublists retain their contracts. This document freezes integration seams for coordinator-assigned **peer direct subagents**, not nested delegation. It is not implementation or acceptance evidence.

## Exclusive ownership

| Peer | Files / responsibility |
|---|---|
| A: discovery engine | New `internal/authorization/discovery*.go` (including engine DB tests). Owns the exported discovery types below, authorization-first SQL, field manifest, relationship labels, rank/count/page. May reuse existing unexported Lease predicates without editing Lease files. No handler, migration, template or navigation edits. |
| B: state | New `internal/liststate/*.go`, `internal/authorization/list_state*.go`, one additive `126_list_state.up.sql` migration (verify numbering remains available). Owns strict generic parser, canonical serialization, presentation persistence and its DB/concurrency/migration tests. No handler or discovery edits. |
| C: HTTP/UI integration | `handlers/search.go`, new handler list-state adapter, ten main-list handlers and their existing service pagination adapters as needed, `services/search.go`, `router.go`, `authorization/navigation_reads.go`, template models/templates/generated templates, `interaction.js`, navigation visibility. Sole owner of shared files and generation. Connects A/B and preserves specialized list projections/actions. Coordinator can split dedicated-list files among additional peers explicitly. |
| D: acceptance | New `handlers/discovery_*_test.go`, `handlers/testdata/browser/discovery-*.cjs`, new API denial tests and durable evidence. Owns production-router/browser/API scenarios; no production changes without handoff. |
| Coordinator/runtime owner | Assignment, interface changes, final review, runtime provisioning and final checks. No delegate starts DB/app/browser runtime before coordinator supplies environment and permission. |

Migration 125 is the highest observed number. Confirm no peer has reserved 126 before writing. Existing preflight reports remain intact. No staging, commits, pushes, issue mutations or other-worktree edits.

## A → C: discovery boundary (package authorization)

```go
type DiscoveryQuery struct {
    Search string
    Types []string
    State string
    Sort string
    Page, PerPage int
    Lease LeaseWorkspaceQuery
}

type DiscoveryResult struct {
    Type string
    ID int64
    Identifier, Title, Description string
    URL string
    Match string
    Archived bool
    Status string
}

type DiscoveryType struct {
    Type, Label string
    CanViewArchived bool
}

type DiscoveryPage struct {
    Query DiscoveryQuery
    Results []DiscoveryResult
    Types []DiscoveryType
    Total int
    HasVisibleRecords bool
    UpdatedAt time.Time
}

func (s *Service) Discover(context.Context, Actor, DiscoveryQuery) (DiscoveryPage, error)
func DiscoveryAllowed(Principal) bool
func ValidateDiscoveryQuery(context.Context, pgx.Tx, Principal, DiscoveryQuery) error
```

`ValidateDiscoveryQuery` is an exported **package function**, with no Service receiver. For atomic preference saving, call it inside B's authorized-save callback with the callback's current `Principal` and `pgx.Tx`, before CAS in that same transaction. It validates query syntax/defaults, discovery eligibility, selected types/archive permissions, Lease facet permissions and independently visible relation targets. It opens no transaction and executes no discovery result query. The caller must enforce browser channel and hold the `WithinActor` authority fence through preference commit; a previously cached principal is not sufficient. `Discover` reuses this validator. It does not mutate its input query.

Engine owner defines these symbols. `DiscoveryAllowed` checks searchable collection entitlement, including Lease base **or Billing**, excluding Time-only and master Context-only authority. C uses it for search entry/navigation rather than changing operational activity semantics. Browser-only boundary rejects API actors. Empty `Types` means all currently discoverable types; explicit unauthorized types reject rather than disappear. Output `Types` contains only eligible choices.

Canonical types and ascending tie order are lexical singular keys: `asset`, `contact`, `customer`, `estimate`, `invoice`, `item`, `job`, `lease`, `location`, `project`. Default `State="active"`; alternatives `archived` and `all`. Explicit non-active mode requires archive permission for every selected discoverable type; mixed-grant users narrow Types first. Defaults: `Sort="relevance"`, `Page=1`, `PerPage=25`; sizes 25/50/100, page 1..1,000,000. Additional combined sorts `name`, `name_desc` use lowercased authorized display title then type/ID, with deterministic descending name only. All combined sorts remain globally paginated.

Lease filters are valid only with exactly `Types=["lease"]`; reuse `LeaseWorkspaceQuery` validation/predicates, lifecycle calculation and independent facet rules. Dedicated Lease remains Number-ordered and retains queues. Search result URLs are authorized destinations: Billing-only Lease links to `/leases/{id}/billing`; no base title/description in that projection. `Match` is safe plain text, including authorized Contact/Location relationship name and identity when it supplied the best match. No HTML is passed from SQL.

Malformed typed queries return an error wrapping `authorization.ErrConflict` (HTTP 400 here); denied filters/types return `ErrForbidden` (403); unavailable dependencies return a non-domain error (safe 503). On every error the page contains **no usable partial results/counts**. Handler retains submitted state independently, never renders partial engine output. No raw database error text.

### Search semantics and consistency

- Exact match rank 0 applies to internal decimal identity and configured public Numbers, plus SKU only with Pricebook visibility. Rank 1 is a prefix of any individual authorized searchable value; rank 2 is an elsewhere substring. Empty search lists authorized candidates. Match literal Unicode text case-insensitively; `%` and `_` have no wildcard meaning.
- Engine folding uses an explicit installed Unicode-capable `pg_catalog` collation (`pg_c_utf8`, then `C.utf8`/`C.UTF-8`, then `und-x-icu`), because an installation initialized with `--no-locale` otherwise silently implements ASCII-only lowercase. Absence fails the whole query rather than weakening case-insensitivity. No extension or locale migration is introduced.
- Use independent scalar terms, not concatenated fields that produce accidental cross-field matches. A record with several matches appears once, using its best rank; ties are type/ID. No per-type prelimit.
- Active applicable custom-field definitions only; text/multiline, number, ISO date and single/multi option labels. Checkbox excluded by DISC-03's enumerated scalar kinds. Retired options retained in an active definition remain searchable recorded labels; retired **definitions** never match. Resolve retained-global applicability with existing ownership semantics. No raw JSON-wide search.
- Built-in identifiers/names/descriptions and applicable stored scalar text/number/date/option fields form an explicit allowlist alongside custom fields (see `discoverySQL.scalarTerms`). Item type is base; SKU/price/tax are Pricebook; unit cost is Cost. Customer billing address is financial; document tax and Job billing type are independently Pricing-gated including row-specific Assigned scope. Lease Billing due date and stored terms require Billing; base dates/custom fields/premises/title require base View. Dates are ISO, instants UTC RFC3339, money two-decimal dollars, other numbers/options use stored scalar spelling/labels. Exclude technical revisions/foreign keys, retired legacy CRM fields, derived financial aggregates/history/child ledgers; never indiscriminately index row JSON.
- Authorization predicates precede matching, ranking, count, existence and pagination. Hidden conversion records are always absent. Related records are independently authorized, company-bound and subject to archived target grants; Customer relationship matches use active children only.
- Use current actor/session/principal, not navigation's cached principal or role strings. Engine chooses a coherent read transaction/snapshot with actor validation and one candidate statement for total, existence and rows (including empty/out-of-range pages). Existing `WithinActor` locking semantics must be inspected before choosing isolation; document actual linearization in A's evidence. No partial enrichment failures.
- Implemented linearization: `WithinActor` obtains installation authority fence 190019 before loading current principal; candidate/matching/count/page/enrichment share one PostgreSQL statement snapshot under Read Committed. Current session/principal is checked again before commit. Authority and fenced domain writers serialize against that transaction; statement visibility handles ordinary concurrent catalog updates consistently. Lease relation-filter target validation is additionally fenced and cannot use a hidden target as an existence oracle.

## B → C: typed URL parsing and persisted state

Keep generic state independent of authorization and domain packages to avoid import cycles. `internal/liststate` owns:

```go
type Spec struct {
    // Allowed key -> validate all its values; repeated values forbidden unless
    // the validator explicitly permits them (combined search "type").
    Fields map[string]func([]string) error
    Defaults url.Values
}
type Query struct {
    Values url.Values // normalized effective values, including defaults
    Explicit bool    // any query-state key was supplied, even an empty value
    Reset bool
}
func Parse(url.Values, Spec) (Query, error)
func Canonical(url.Values) string
func Persistent(url.Values) url.Values
```

`Parse` rejects unknown keys, malformed/duplicate singleton values, invalid enums/ranges and oversized inputs. Common keys are `q`, `sort`, `page`, `per_page`, `state`; repeated `type` is combined-search only and duplicate types reject. `reset=1` is the sole Reset control and cannot combine with query-state keys. B may provide common validators to avoid ten copies. C defines per-route specs preserving legitimate existing dedicated filter names and sort choices; do not silently discard existing filters. `Persistent` deep-copies and removes `page`, `reset`, and presentation transport keys; no scroll or result content is stored. Canonical URLs always include explicit query state, including `q=` when empty. Maximum query text is 500 Unicode characters; maximum serialized state is 16 KiB.

Package authorization owns these storage methods, implemented by B:

```go
type ListState struct {
    Values url.Values
    Revision int64
}
func (s *Service) LoadListState(context.Context, Actor, string) (ListState, error)
func (s *Service) SaveListState(context.Context, Actor, string, url.Values, int64) (ListState, error)
func (s *Service) ResetListState(context.Context, Actor, string, int64) (ListState, error)
```

Last argument is the expected revision, 0 for absent state. Methods freshly bind actor to company/user and accept only list keys `search`, `customers`, `contacts`, `locations`, `assets`, `jobs`, `projects`, `estimates`, `invoices`, `leases`, `items`. Save increments revision under CAS; Reset stores an empty tombstone with incremented revision, preventing an already-admitted stale save from resurrecting it. Conflict wraps `ErrConflict`. No session authority cached in preferences; version column starts at 1. Search/presentation state is not business activity and is not audited (DISC-06); do not log free-text searches.

C orchestration:

1. Parse explicit URL first; malformed input fails without consulting defaults/saved state to broaden it.
2. Load current user's revision. If no explicit state, parse/validate persisted values through the same route spec; otherwise ignore all saved query values.
3. Reauthorize and run the effective query through its read boundary.
4. GET never writes durable preferences. A successful rendered page carries its canonical state and originally loaded revision. A same-origin CSRF-protected `POST /{list}/state` reparses that state and reruns the same list read to establish query availability. That earlier read is **not** commit authority. B's `SaveAuthorizedListState(ctx, actor, key, values, expected, validate func(pgx.Tx, Principal) error)` holds one `WithinActor` fence/transaction across the fresh validation callback and CAS write. C invokes A's transaction-aware `ValidateDiscoveryQuery` for search and B's `ValidateCollectionListQuery` for the dedicated routes; validators share actual domain checks (collection/types, archive, facets, scopes and relation targets), use only the supplied transaction/current Principal, and never call a nested public read transaction. JavaScript submits this presentation-only POST; without JavaScript the page offers Remember filters. CAS conflict does not retry with a newer revision, so a stale response cannot overwrite Reset/newer state. Preference-unavailable is visibly distinguished from read success, retains filters, hides results when authority is uncertain, and never reports persistence success. No client assertion of prior authorization is trusted.
5. Reset uses CSRF-protected POST in the HTTP adapter and redirects to an explicit default-state URL; no business mutation. A GET reset control can present/submit the protected action, but must not perform an unprotected state mutation.

An obsolete/now-unauthorized saved state remains blocked with an actionable Reset; never silently clears or broadens. Page and scroll are transient browser history only. Existing Lease remembered cookie stops being authoritative once server state is integrated.

## Browser/error contract

C keeps the existing single history owner: URL/scroll only, current-authority refresh on Back/BFCache, no stored result HTML. In-flight updates label any displayed prior results as previous-query results; responses are latest-request-wins. Authority failure/session expiry hides old results immediately. If network/DB uncertainty cannot revalidate old authority, hide old data and state that previous results are unavailable; do not paint zero matches. Preserve filter inputs and Retry, with status announcements. D tests actual DOM history, ordering, failure and revocation behavior.

## Next handoff

Coordinator can launch B/C/D against these names while assigning A to the discovery engine. Any necessary interface amendment is sent to coordinator before edits to another owner's files. Runtime verification awaits the separate runtime owner's durable environment. This contract-only pass has not run build, lint, DB, API or browser tests.

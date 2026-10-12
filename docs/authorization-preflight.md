# Customer authorization and Administrator-reviewed cutover

Migration 055 expands authorization non-destructively. Existing installations
must complete an authenticated Product Administrator review at
`/settings/authorization` before Customer authorization becomes available.
The standalone inventory command remains read-only; it never approves cutover.

## Run the inventory

Build from the source checkout:

```sh
go build -o /path/to/freefsm-auth-preflight ./cmd/freefsm-auth-preflight
FREEFSM_AUTH_PREFLIGHT_DATABASE_URL='postgresql://…' /path/to/freefsm-auth-preflight
```

The database URL must be explicitly supplied; there is no default host database.
Use the intended application schema/search path and a connection with SELECT
access to its users and migration ledger. The command runs a read-only,
repeatable-read transaction and has a 30-second deadline. It does not start the
application, migrate, write audit, seed defaults, or acquire product authority.
It does not require superuser access.

JSON is written to stdout; diagnostics go to stderr. Exit codes:

| Code | Meaning |
| --- | --- |
| 0 | Supported, fully migrated legacy schema with no users and no identified review issue; **not cutover approval** |
| 1 | Configuration, database, schema/read-access, timeout, or output failure; no complete inventory may be assumed |
| 2 | Inventory emitted, explicit review required |

When using `go run`, Go wraps a command's nonzero exit status; use the built
binary if a script needs to distinguish codes 1 and 2.

Treat the report as identity data: it includes names, emails, company and user
IDs. It excludes credential hashes, tokens, session contents and grant values
from unrecognized storage. `password_present` means only a nonempty stored
value. It does **not** prove a usable password, completed onboarding, or an
authenticatable Administrator.

The report records the migration ledger, table names, stored users, legacy Role
discriminator values and their user IDs, and actual Role check constraints.
`absent_in_supported_legacy_schema` distinguishes missing grant history from a
recorded empty grant set. A changed migration ledger, unexpected user columns,
or tables named for Roles/grants/permissions require inspection; unknown grant
storage is never treated as an empty grant set. This is an inventory of the
known schema through migration 054, not a general interpreter for arbitrary
forks. Review all reported tables when inventorying a fork.

The SHA-256 fingerprint binds exported observations only. It is neither signed
approval nor a commit-time revision, and excludes credentials. A cutover must
recheck the current database and authentication state under appropriate locks.

## Observed legacy authorization mappings

These describe source behavior at baseline `580e95a`, **not approved v1 grants**:

| Recorded/code fact | Source |
| --- | --- |
| Users have a `role` discriminator constrained to `admin`, `dispatcher`, `tech`; no Role identity or grant table is defined | `internal/database/migrations/001_users.up.sql`, migrations through 054 |
| Browser admin middleware compares `role` with `admin`; operational middleware compares `admin` or `dispatcher` | `internal/middleware/auth.go` |
| User creation and update accept the discriminator through user forms/services; they do not enforce a protected Role identity or last-Administrator invariant | `internal/handlers/users.go`, `internal/services/user.go` |
| Setup creates a user with `role='admin'` | `internal/handlers/setup.go` |
| Customer policy allows direct Customer assignment or linkage through an assigned active Job for technician aliases | `internal/services/policy.go` |
| Customer Show passes the full Customer detail, master custom fields, files, contacts and locations to its template after the legacy policy check | `internal/handlers/customers.go` |
| Search and activity have additional legacy Customer authorization/projection paths | `internal/services/search.go`, `internal/services/activity_resolver.go` |
| Browser session validation and API authentication follow separate paths | `internal/services/session.go`, `internal/middleware/auth.go`, `internal/api/v1/auth.go` |

No recorded grants can be reconstructed from these labels. The production
upgrade must preserve the discriminator as a recorded legacy fact, rather than
reinterpret it as a v1 grant set or invent grant-edit history.

## Approved existing-installation workflow

Run the inventory before migrating a legacy installation, then inspect its
recorded identities, role discriminator values, and any unknown storage. The
inventory fingerprint is evidence, not authority. After migration:

1. An existing authenticated Product Administrator opens **Authorization
   review**. Migration records the identities that had the actual legacy
   Administrator discriminator. This is the only transitional use of that
   discriminator: those identities can review/approve, but cannot use the
   ordinary sealed administration editors until cutover.
2. Create or edit explicit Role grant sets. Existing-installation Dispatcher
   and Technician templates have **no grants**. Empty grants (`[]`) are valid.
   No legacy label is translated into permissions or membership automatically.
3. Explicitly select and approve a new Role for **each user**, including the
   reviewer. The page shows the Role's grants and binds approval to a consistent
   review snapshot. Changes to identities or grants invalidate stale approvals.
4. Select at least one enabled, authenticatable user for the protected
   Administrator Role. Type `APPROVE CUTOVER` and activate the reviewed decisions.
   Missing/stale decisions or loss of the last authenticatable Administrator
   reject the entire cutover. User IDs, legacy discriminator strings, Customer
   IDs, links, and recorded fields remain intact.
5. After activation, only protected Role identity authorizes sealed editors.
   The transitional approver list no longer grants anything. Membership and
   effective permissions survive process restart without reseeding.

A Deployment Operator running the CLI gains no Product User authority. If no
existing Product Administrator can authenticate, cutover stays blocked; this
flow cannot create an operator account or bypass product authentication.

## Fresh installations and Role management

Fresh installations receive the canonical Administrator, Dispatcher and
Technician grant sets. The uninitialized first Administrator bootstrap is the
only automatic protected membership. Subsequent users start in an empty-grant
Role; assign their Role explicitly in Authorization. If all empty-grant Roles
have been removed, create one before adding a user. Existing installations
never receive fresh-install nonprotected defaults.

The protected Role identity/name/grants cannot be renamed, removed or weakened.
New catalog permissions automatically reach only protected Administrator, at
their broadest valid scope. Other Roles may be renamed or atomically removed
with explicit replacement of assigned users. Role edits use expected revisions;
stale editors cannot restore concurrently revoked grants. Legacy user forms no
longer offer authority assignment; direct changes to `users.role` are rejected.

The editor accepts a JSON array of closed catalog entries, for example:

```json
[
  {"permission":"customer.view","scope":"A"},
  {"permission":"customer.create","scope":"A"},
  {"permission":"customer.edit","scope":"A"}
]
```

| Scope | Meaning |
| --- | --- |
| A | All Records, within the application's recorded company boundary |
| J | Currently assigned Jobs / explicit Job-bound actions |
| C | Closed, read-only Assigned Job Context |
| O | Own Records, using the domain's stable subject/author/uploader anchor |
| U | Unscoped configuration |
| S | Sealed Administrator authority, never assignable to ordinary Roles |

Unsupported permission/scope pairs, duplicate pairs and missing View prerequisites
reject the complete grant edit. Narrowing View must also remove incompatible
dependent actions. Customer Create/Edit require Customer View A. Customer View C
requires Job View J or A; it never permits master editing.

A Role can combine independent valid scopes (for example Time View O and J)
without widening them to All Records. Each scope retains its own target anchor.

## Migrated versus legacy boundary

`internal/authorization` owns Customer operational View/Create/Edit, closed
Customer context, the explicit grant model and sealed authority management.
Authenticated web and API requests carry server-derived actor/session context.
Customer query filtering runs before counts and limits across existing Ent
consumers (lists, search, lookups, activity, related-record and document-source
projections). Ordinary Customer queries require View A; Customer assignment and
known IDs do not authorize them. Invisible Customers return 404; denied actions
on visible Customers return 403. Existing Customer lifecycle routes additionally
require their independent lifecycle grants.

Assigned Job context is available at `/jobs/{id}/customer-context` in the browser
and `/api/v1/jobs/{id}/customer-context` for bearer clients. It requires explicit
current assignment and a non-archived Job, regardless of descriptive Job Status.
The DTO contains only identity/name/kind/main phone/email and Service Notes. It
contains no billing/tax/financial/internal-note/custom-field/sibling data. Service
Notes is a new independent field; existing internal notes are not reclassified
or copied into it.

Existing Job-bound document creation/editing uses a separate selected-input
projection: current Customer identity plus an effective tax rate authorized by
captured-pricing View. This retains tax-exemption effects without returning the
master exemption flag, billing details, internal notes or sibling Customers.
Its Customer selector contains only the explicit assigned Job's Customer when
the user has Context rather than All Records. Document renderers similarly use
only allowed contextual names/contact data instead of reopening the master.

The catalog also declares future-domain permissions and initial grants. Their
business actions migrate in their owning slices. Jobs, documents, settlement,
collaboration and other masters retain their legacy action rules; those rules
cannot bypass the migrated Customer boundary or authorize Customer details
writes. In particular, the optional legacy settlement/related-work panels retain
their own authority checks; Customer operational access alone does not authorize
financial actions. Billing/tax fields and the financial panel additionally
require Customer financial View. Closed Context never enters the master page.

Customer details writes use one PostgreSQL transaction for effects, ordinary
activity projection and immutable `authorization_audit`. Authority/session state
is reloaded under a transaction mutex shared by grants, Roles, user and session
writers, with a final identity/session check before commit. A revocation that
commits before the mutation's authorization check rejects the whole action;
once a mutation holds the lock, revocations serialize after its commit. A
deferred database invariant protects the last enabled authenticatable
Administrator even through legacy writers. Audit failure rolls back effects;
denials produce security logs, not success audit.

## Evidence

- `internal/authorization/authorization_integration_test.go`: actual migrations,
  explicit review and stale snapshots, scope/prerequisite rejection, immutable
  protected authority, defaults/new permissions, atomic Role replacement,
  revocation contention, audit failure, last-Administrator concurrency and a
  fresh-process restart probe retaining identities and relationships.
- `internal/handlers/customer_permissions_integration_test.go`: production
  router/session/DB cutover, editor denial, custom/renamed/empty grants, Customer
  mutation/audit, 403/404 distinction, financial projection denial, API/browser
  context allowlist, assignment loss and filtered search.
- `TestCustomerPermissionsPersistedBrowser`: Chromium through production HTTP,
  CSRF and PostgreSQL; persisted create/edit, hidden restricted inputs and a
  protected Administrator's grant revocation through the real Role editor.

Run the persisted browser test with `BROWSER_TESTS=1`, an installed Chromium
`BROWSER_EXECUTABLE`, Playwright dependencies, and an explicit disposable
`FREEFSM_TEST_DATABASE_URL`. Ordinary `make test` skips browser opt-ins; those
skips are not acceptance evidence.

The coordinator must verify the completed ticket before closure.

## Catalog correction and shared audit (migrations 056–057)

Migration 056 corrects export scopes and View/facet prerequisites using a closed
product-defined prerequisite table. Parent-bounded grants need an eligible
compatible parent View; the actual resource/facet must still be authorized at
runtime. Recorded invalid grants are preserved but do not provide effective
authority. The Role editor reports missing prerequisites for explicit repair;
pending approvals for affected Roles are invalidated. Missing views are never
auto-granted to existing nonprotected Roles. Job runtime authorization remains
with its owning slice.

Migration 057 shares `internal/protectedaudit` between authorization mutations
and host recovery. Product events retain their effective Role and scope; host
recovery records `actor_kind=deployment-operator` and the host UID, with NULL
product actor, Role and scope. The `action` column preserves the former
`permission` values and also represents the distinct host recovery action.
Credentials, session/recovery revocation, ordinary activity and immutable recovery
audit commit together. Historical ordinary activity is not promoted into invented
immutable history. No audit browsing interface is introduced.

See `docs/v1-integration-19-107.md` for the full catalog review and regression evidence.

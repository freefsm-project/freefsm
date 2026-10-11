# Legacy authorization inventory — ticket #19 prerequisite

**Status: review tooling only. Ticket #19 is not implemented end to end.**
The application still uses legacy authorization. This command neither expands
the authorization schema nor enables the Customer permission cutover.

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

## Review required before existing-installation cutover

For an inventory fingerprint, the review must explicitly answer:

1. Which existing user identities map to which new Role identities, and which
   enabled, authenticatable identity or identities retain protected
   Administrator authority?
2. What explicit grants, including an explicitly empty set where appropriate,
   are approved for existing nonprotected Roles? Fresh-install defaults are not
   an upgrade mapping.
3. For forks or unknown storage, what recorded Role/grant facts and relationships
   must be preserved, and what evidence resolves each unsupported mapping?

The reviewed mapping import, authority to approve it, and transactional cutover
are not implemented by this command. A Deployment Operator running an inventory
does not become a Product User or gain authority to edit product grants.

## Remaining ticket acceptance

Only the known-schema read-only inventory prerequisite has implementation and
new database/CLI tests. These remain open:

- Explicit Role identities, closed permission catalog and scope validation,
  View prerequisites, protected Administrator identity/name/grants and all editor
  seals, including existing user Role assignment routes.
- Canonical fresh-install grants, reviewed existing-installation mapping/import,
  startup cutover guard, and upgrade/restart relationship preservation.
- Customer View/Create/Edit on every browser, HTTP/API, lookup, search, activity,
  dashboard and related-record projection; closed Assigned Job Context allowlist
  including a distinct Service Notes field; no Customer-assignment authority.
- Atomic mutations and immutable audit; grant narrowing/revocation at commit;
  last-enabled-authenticatable-Administrator concurrency protection.
- Production-route denial/failure/concurrency tests, visible-denied versus
  invisible-resource results, persisted Customer browser journeys, and complete
  acceptance evidence. Existing legacy tests passing do not prove v1 compliance.

The coordinator must verify the completed ticket before closure.

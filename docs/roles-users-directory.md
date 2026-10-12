# Roles, users and operational directory (#20)

## Product surfaces

- **Authorization** (`/settings/authorization`): protected Administrators create
  and rename Roles, select grants from the closed catalog, assign one Role per
  user, and remove Roles with an explicit atomic replacement. Empty grants are
  supported. The editor shows valid scopes and required Views/facets; invalid
  combinations, stale Role revisions and dependent-grant narrowing reject the
  entire change. Administrator identity and authority remain protected by #19.
- **Users** (`/users`): sealed identity editing, explicit Role choice on creation,
  enable/disable, temporary-password reset, require credential change, revoke all
  sessions, welcome resend and recovery resend. Displayed Roles resolve current
  Role identities rather than legacy labels. Identity forms reject security-field
  injection. Administrator-set passwords require replacement before product use.
- **Operational directory** (`/directory`): `directory.view/A` only. HTML and JSON
  expose exactly identity (ID/name/email) and availability. There are no account
  links, Roles, credential, recovery or session facets. Field API administrative
  and directory routes remain unavailable; session revocation affects bearer and
  browser sessions alike.

## Transactions and migration

Account actions reuse the existing authorization mutex, protected Administrator
identity, commit-time principal checks and immutable audit writer. Account-link
services use `MutateAccountLink` to commit token changes and sealed audit together;
token purpose and expiry remain owned by the invitation/recovery services. Audit
failure rolls back identity edits and replacement links. No credential or token
value is written to the audit ledger. SMTP failure remains an explicit send
failure, not a claim of delivery.

Forward migration `059_directory_assignment_availability.up.sql` changes no
existing record or grant. It serializes new assignment writes with disablement
and rejects unavailable or foreign-company people. Assignment updates retain
existing relationships (including disabled users) instead of deleting and
recreating all rows. Moves now commit schedule and assignment effects together.
Planning selection rechecks Directory View and session authority while the
assignment transaction holds the same mutex. Active selectors omit account Role
labels, and existing inactive assignment options retain their unavailable state.

The #19 preflight/review/cutover and original author/uploader anchors are retained.
No migration chooses legacy Role mappings, grants new nonprotected authority, or
fabricates historical Role-change events. Job action authorization and reviewed
dispatch remain in their owning slices; this change covers the operational
person input and its availability/authority race.

## Acceptance evidence

`internal/handlers/directory_permissions_integration_test.go` exercises migrated
PostgreSQL with the production router and public domain services:

- directory allowlist, denied account links/endpoints, hidden security fields;
- closed catalog UI, unsupported scopes and missing prerequisites, empty Roles;
- audited identity edits and audit-storage rollback;
- explicit Role creation choice, temporary credential reset and revocation;
- API surface denial and browser/mobile session invalidation;
- new disabled assignment denial, retained assignments, racing disablement and
  revoked Directory View, plus failed-move schedule rollback;
- audited welcome/recovery replacement and preservation of the old link on
  denial or audit failure;
- last-Administrator rejection and concurrent self-disable through HTTP;
- persisted CSRF-protected Chromium Role create/assign/rename, identity edit,
  directory nondisclosure and unavailable-user journey, read again by a new router.

The existing `internal/authorization` catalog, defaults, upgrade, replacement,
protected identity, session-expiry, audit and concurrency suites are also run.
The Customer permission browser regression uses the new catalog controls.

Execution commands, output paths, exact commits and opt-in skips are recorded in
`/tmp/opencode/freefsm-v1-context/ticket-020-result.md` for integration review.

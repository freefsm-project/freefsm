# Integration acceptance: #19 and #107

Local integration branch: `integration/v1-spec-18`. No issues closed or pushes.
The original checkout was not modified. Input commits, merged in this order:

- #19: `ccd1d73dc86817117a39fb61627096eff79ebd61` (includes `ecfcebf`).
- #107: `4d61e26d81e2a594ce7f61a064ed191a0b773940` (includes `dda1f44`).

Git merged without textual conflicts. Integration changes replace recovery's
legacy discriminator with protected Role identity and require completed cutover.
The authorization mutex is acquired before table locks covering authorization
state, Roles, password policy, users and credentials. Recovery assigns no Role or
grants. Existing last-Administrator invariants remain in force.

Recovery fixtures now use valid bootstrap credentials and a replacement protected
Administrator for demotion/disablement cases. A real pre-055 installation is
reviewed through `Review`, `ApproveUser` and `Cutover`: a former Dispatcher receives
protected identity, and the former Admin receives empty grants. CLI tests deny
legacy authority before/after cutover and recover the new protected identity.
Chromium then logs in, replaces the temporary password, creates persisted
Customer/Job records and checks their lists. Customer redirect matching uses its
pathname rather than requiring the legacy flash query parameter.

## Executed checks

New disposable database on the existing isolated #19 cluster:

```sh
createdb -h 127.0.0.1 -p 55419 -U adam freefsm_v1_merge_18
make lint
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_merge_18?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55419/postgres?sslmode=disable' \
make test

NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_merge_18?sslmode=disable' \
go test -race ./cmd/freefsm ./internal/handlers \
  -run '^(TestInstallationBrowserRecoveryAndCoreWorkflows|TestCustomerPermissionsPersistedBrowser|TestHostRecoveryAfterReviewedCutover)$' -count=1 -v
```

Lint and the full race-enabled suite passed. Backup extension and real restore
tests ran. Full-suite output is retained locally at
`/home/adam/.local/share/opencode/tool-output/tool_12d8b93470018mpHl5fCuBWAaM`.
The final-tree rerun also passed (unchanged packages used Go's test cache):
`/home/adam/.local/share/opencode/tool-output/tool_12d905d24001IuCFvN0tr9Z4QK`.
Persisted authorization and post-cutover recovery Chromium checks passed after
the integration fixture/redirect corrections. An initial malformed credential
fixture and invalid legacy role spelling failed, were corrected and rerun.

Full-suite skips: PostgreSQL <16 negative case (this cluster is 18), opt-in
Chromium tests (relevant persisted tests run separately), nginx scale, and helper
entry points that their parent subprocess tests invoke. Existing intercepted
backup/template Chromium tests and native OS/reboot/release matrix were not
rerun here. These are not claimed as passes.

## #19 acceptance assessment and implementer follow-up

Inspection of migration 055, authorization services/boundaries, production HTTP
tests and the executed tests supports explicit Customer grants, scoped closed
Job context, legacy editor sealing, reviewed upgrade, stale-review rejection,
protected identity/last-Admin races, transactional Customer audit rollback,
revocation/session-expiry rejection, and persisted browser/API nondisclosure.
This is a focused integration assessment, not blanket closure approval.

**Catalog defects reproduced during merge (resolved by follow-up migration 056):**

1. `055_authorization_expansion.up.sql:48` creates Estimate/Invoice export only
   at A, while parent PER-12 specifies J and A. A Role with `estimate.view/J` plus
   `estimate.export/J` (likewise Invoice) is rejected by `SaveRole` as unsupported.
2. The same migration's `job.export` entries (lines 67–68) have no View
   prerequisite. `SaveRole` accepts `job.export/J` alone, contrary to AUTH-02 and
   PER-12's compatible View requirement.

Both were reproduced through the actual `SaveRole` API against migration 055 in
the isolated merge DB, not inferred solely from the handoff report. A temporary
acceptance probe failed with these three observations and is retained outside
the worktree at
`/tmp/opencode/freefsm-v1-context/merge-019-catalog-probe_test.go`; copy it into
`internal/authorization` and run `TestMergeCatalogAcceptanceProbe` to reproduce.
These were pre-existing foundation/catalog findings; no unrelated domain feature
was added during merge. Their scenarios are now permanent public-`SaveRole`
regressions in `internal/authorization/catalog_contract_test.go`.

**Explicit remaining boundaries:**

- Legacy Job action rules still apply. A protected Administrator with legacy
  `tech` can recover and create a Customer but cannot open Job creation. This was
  observed in Chromium; the passing combined journey uses legacy `dispatcher`.
  This is consistent with #19's declared future-domain boundary, not evidence
  that Job authorization has migrated or all equivalent-role journeys pass.
- Host recovery now commits both its ordinary activity projection and a shared
  immutable audit event with credential changes/revocation (migration 057).
  The event names the Deployment Operator's host UID, not the target as an
  acting Product User; actor ID, effective Role and product scope are NULL.
  Immutable-audit failure rolls back the credential effects and activity too.
- Pending cutover with no recoverable reviewed protected identity is denied.
  Recovery does not choose a mapping or turn a legacy name into authority.
- #107's native Linux/FreeBSD service, reboot, release artifact, topology/egress
  and scale gates remain as documented in native installation acceptance.

## Implementer follow-up: full catalog and protected recovery audit

The follow-up starts from merged integration `86e86cb`. It uses forward migrations,
leaving migration 055 and recorded grants/history intact.

Catalog review used canonical PER-01–14 plus the S6 permission resolution and
its S8/S11/S13/S16 amendments. In addition to the original three probe failures:

| Correction | Contract |
| --- | --- |
| Estimate/Invoice Export J added; Job Export requires Job View | PER-12, AUTH-02 |
| GPS Export requires GPS View, not merely Time View | PER-08/12 |
| Restore requires View archived (which requires base View) | PER-11, AUTH-02 |
| Captured pricing accepts compatible Job **or document** View | PER-06; Customer-only document access does not require unrelated Job View |
| Collaboration actions require an eligible visible parent | PER-09; Own anchors intersect parent visibility, and Context never unlocks master collaboration |
| Operational activity export requires compatible parent View | PER-12; facets and the actual parent remain independently checked by owning domains |
| Credit application/refund require Credit View; application also Invoice View | PER-07, independently authorized selected inputs |
| Reversals require settlement-history View | PER-07 |
| Audit Export/security Manage name their corresponding sealed View | PER-13/14 |

Fixed parent alternatives are catalog metadata, not user-authored policies. A
matching Invoice View never substitutes for Job View on an actual Job action.
No Job command, route or runtime action rule was migrated in this follow-up.
Optional financial fields on a resource export still require their own facet
View; the export grant does not manufacture or imply those grants.

Existing invalid combinations remain recorded, lose effective authority for
unsatisfied dependent actions, and are marked for explicit Administrator repair.
Affected pending approvals are invalidated. Neither migration adds missing views
to nonprotected Roles; fresh Technician still receives no exports. Invalid Role
sets cannot be approved into a cutover. Repeated migration/restart preserves
identities and recorded grant facts.

`internal/protectedaudit` is a shared transaction-bound writer. Product events
retain their actor/effective Role and permission/action scope. Host recovery uses
a separate, purpose-specific entry point with no Product User or Role attribution.
The old audit `permission` column is renamed `action` without rewriting values;
existing events remain product-user facts. Host recovery has no product scope.
UPDATE, DELETE and TRUNCATE of the immutable ledger are rejected. There is no audit
browsing UI, and old mutable activity is not backfilled as invented immutable history.

### Follow-up evidence

- Entire closed catalog scope matrix, valid/invalid assignments, every base View
  prerequisite, representative parent/facet alternatives, scope narrowing and
  canonical export/sealed defaults: `catalog_scopes_test.go` and
  `catalog_contract_test.go`.
- Upgrade from 055 preserves raw grants and old audit values, denies invalid
  effective authority, invalidates approvals and permits explicit repair:
  `catalog_upgrade_test.go`.
- Real CLI/non-superuser PostgreSQL tests assert host attribution, credential
  redaction, immutable ledger protection, post-cutover recovery, and rollback on
  failure of **either** audit write: `cmd/freefsm/installation_integration_test.go`.
- `make lint` and full race-enabled `make test` passed with explicit isolated
  URLs `postgresql://adam@127.0.0.1:55419/freefsm_v1_19?sslmode=disable` and
  backup administrator `postgresql://adam@127.0.0.1:55419/postgres?sslmode=disable`.
  Full output: `/home/adam/.local/share/opencode/tool-output/tool_12dbbedc3001e5YUHvZf8To0Tb`.
- The persisted Customer authorization and installation recovery/core-workflow
  Chromium tests passed, as did `TestHostRecoveryAfterReviewedCutover`, using the
  browser command above with the isolated `freefsm_v1_19` database.

All identified #19 catalog and shared host-audit findings are addressed. #19 is
implementation-complete for coordinator verification within its declared scope;
this is not issue closure, a Job-domain migration, or completion of #107's native
release/OS acceptance gates. Skips and wider matrix limits above still apply.

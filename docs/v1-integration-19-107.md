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

**Confirmed catalog defects need implementer disposition before #19 closure:**

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
These are pre-existing foundation/catalog findings; no unrelated domain feature
was added during merge. Existing green tests do not cover these catalog cases.

**Explicit remaining boundaries:**

- Legacy Job action rules still apply. A protected Administrator with legacy
  `tech` can recover and create a Customer but cannot open Job creation. This was
  observed in Chromium; the passing combined journey uses legacy `dispatcher`.
  This is consistent with #19's declared future-domain boundary, not evidence
  that Job authorization has migrated or all equivalent-role journeys pass.
- Host recovery's event commits atomically in `activity_logs`, including tested
  audit-insert rollback, but does not enter #19's immutable `authorization_audit`.
  Immutability for this host-attributed event is not established; the owning
  implementers need to resolve it before claiming shared AUTH-04 acceptance.
- Pending cutover with no recoverable reviewed protected identity is denied.
  Recovery does not choose a mapping or turn a legacy name into authority.
- #107's native Linux/FreeBSD service, reboot, release artifact, topology/egress
  and scale gates remain as documented in native installation acceptance.

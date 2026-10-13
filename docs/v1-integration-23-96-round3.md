# v1 integration round 3: sessions, fields, Projects, Assets, Files and exports

## Scope and provenance

This round starts at `6fb21df` on `integration/v1-spec-18` and preserves these
exact implementation tips as ancestors:

| Ticket | Tip | Merge |
|---|---|---|
| #30 | `7f8e3c3` | `6bf3c6a` |
| #46 | `0240f61` | `54cbdda` |
| #23 | `79f007f` | `1ccfffe` |
| #32 | `bf70712` | `0dcfbb8` |
| #34 | `e4bd564` | `d615222` |
| #96 | `eb6e338` | `5a6a4cd` |

Worktree: `.worktrees/v1-spec-18/integration`. Durable execution evidence:
`.agent-state/v1-spec-18/merge-round3/` (called **evidence root** below).
Normative issue/spec snapshots and original implementer reports remain under
`.agent-state/v1-spec-18/context/`. Historical temporary paths in those reports
must be translated with `.agent-state/v1-spec-18/relocation-manifest.json`.

The original dirty checkout is outside this work. Its recorded file hashes are
checked by `verify-original.py`; neither source nor index there is used for merges.
All commits are local. No issue closure or push is part of this round.

## Composition and repairs

The Asset merge required five source/generated conflict resolutions. Delegated
repair retained both typed Project and Asset operational projections with separate
permission parameters, SQL scans and template sections. A same-Job HTTP/API
regression now exercises Asset-only, both, and Project-only grants, secret-field
exclusion and assignment loss. Both immutable ownership assertions remain.
Session inventory before migration 052 and reviewed Asset preflight/cutover both
remain. Ent and templ output were regenerated. Equal numeric migration prefixes
are intentionally retained: the runner orders and records complete filenames.

Independent read-only reviews are retained as `review-core.md`, `review-domain.md`
and `review-domain-final.md`. The initial domain review found three real gaps;
delegated implementation reproduced and corrected all three:

1. **Independent lifecycle controls:** Project Archive/Restore no longer pass
   through legacy role middleware. Project and Asset Archive discovery uses its
   independent action; Restore discovery requires compatible View, archived View,
   Restore and current dependency eligibility, including for Administrators.
   Committing domain checks and atomic audit remain authoritative.
2. **Retained Project Location:** an unavailable current Location has an explicit,
   nondisclosing “keep” selection. Unrelated saves and validation rerenders retain
   it; explicit clear remains distinct. The creation-only option reload cannot
   overwrite the Edit selection. Independent Job Locations remain unchanged.
3. **Semantic workflow reopening:** moving an in-use Project Status into an
   unfinished category locks and checks every referencing Project, including
   archived references, under the existing configuration fence. Confirmation
   cannot bypass Location repair. Rejection and audit failure leave the complete
   category/order/activity effect uncommitted.

`conflict-result.md` and `repair-result.md` contain file lists and exact commands.
`repair-red-2.log` and `repair-browser-red.log` record the behavioral failures.
`repair-green-race.log` passes all five new HTTP/concurrency/browser regressions;
`repair-browser-final.log` passes the final browser harness. Nonprotected users
actually click Project and Asset Archive/Restore; browser saves retain both
permission-hidden and archived Locations before an explicit clear.

The post-merge repairs and final typed-field browser correction are committed as
`b89043c` (`fix: compose Project and Asset lifecycle acceptance`). The evidence
document is committed separately; its commit is the final integration tip.

## Scoped acceptance matrix

The following source/test assessments include the integration repairs. Execution
gates and exclusions are recorded separately below; downstream boundaries are
not counted as completed requirements.

| Ticket / criteria | Scoped assessment and evidence |
|---|---|
| #23 AC1–2 | Exact seven-day browser and thirty-day mobile lifetimes; mobile renewal only after successful online authorization; own list/revoke/current/all and sealed other-user management. Session HTTP and persisted-browser tests. |
| #23 AC3–5 | Security-triggered terminal revocation, restore invalidation, fenced in-flight mutation/renewal races, duplicate logout, secret-free summaries and expiry sign-in with no interrupted-action replay. Credential/session/backup tests and browser journey. |
| #23 AC6–8 | Epoch distinguishes ordinary purge from restore-locked pending review. Executable reference preserves original target identity and requires fresh current authority. Pre-052 unknown facts remain NULL/review-required; retained attribution and Role-independent own access survive reconstruction. |
| #23 AC9–10 | Current own/sealed checks and protected audit commit atomically. Additive inventory/epoch and retained revocation rows; no ordinary-Role grants or invented activity. |
| #30 AC1–3 | Retirement/history, retained options, requiredness suspension, reactivation without backfill/replay, cosmetic tolerance and stored/captured/hidden dependency guards. Field evolution/offline/race tests. |
| #30 AC4–5 | Explicit compatible one-to-one keys now apply consistently to owned and retained-global definitions. Exact precision/ordered options and captured-cycle reversion survive later configuration; incomplete captures require review. |
| #30 AC6–8 | Required Invoice preview, paired-source precedence, unpaired defaults, independent Invoice Edit, recovery exemptions and reviewed terminal field operations. Genuine released NULL-company history blocks destructive changes until explicit valid review and preserves original snapshot bytes. |
| #30 AC9–10 | Two-company GET/success/replay/conflict revision maps exclude foreign definitions; foreign/nonexistent submitted IDs are nondisclosing. Additive migration rejects ambiguous effective keys without rewriting history or grants. Both round-2 root findings are repaired. |
| #32 AC1–4 | Minimal creation, configured New/zero progress, immutable Customer, integer manual progress, Completed-derived 100 with retained manual value, local planned dates and overdue semantics. HTTP, race, upgrade and persisted-browser tests. |
| #32 AC5–6 | Direct paginated Jobs retain independent Locations. Completed/Canceled archive and active-Customer restore preserve history without reopening. Independent lifecycle UI, retained unavailable Location and all semantic reopening paths include the three repairs above. |
| #32 AC7–10 | Original progress/date/ownership evidence and unknown prior manual value remain explicit. Current inputs, closed assigned context, grant/dependency races and atomic audit retain nondisclosure and non-destructive semantics. Combined Project/Asset projection is exercised. |
| #34 AC1–5 | Immutable retained Number/class, explicit owner/placement, unplaced cardinality, active Type, optional descriptive Status, repeated serial and descriptive fields. Concurrent/archived uniqueness, ordinary reassociation denial and retired-reference behavior are covered. |
| #34 AC6–8 | Snapshot-bound reviewed legacy identity resolution preserves known mappings/references without invented class or association dates. Master/picker authority remains separate from the exact assigned context allowlist. Archive/vocabulary/grant races and real process restart are covered. |
| #34 AC9–10 | Independent domain commands, repaired Archive/Restore discovery, current dependency checks and atomic audit. Additive cutover grants nothing to ordinary Roles and preserves immutable review evidence. |
| #46 AC1 | Accepted for Customer, Project, Job, Asset, Estimate and Invoice; excluded standalone collections remain excluded. **Lease wiring remains downstream #65.** |
| #46 AC2–5 | Representative validated format corpus including genuine legacy Office, configurable 25 MiB default, ten-file queue, independent outcomes, validated durable publication, authorized preview/download and fixed-extension rename/new replacement identity. Lost-response browser reconciliation does not publish twice. |
| #46 AC6,9 | Retained removal facts/bytes and existing immutable Delivery PDF survive encrypted recovery. **Additional Delivery File snapshots remain downstream #56**; their future behavior is not certified by the PDF fixture. |
| #46 AC7–8 | Origin is immutable authenticated mobile-session identity, not device label or transfer ID. Different same-user clients conflict; exact same-client replay across transfer/router reconstruction returns the same File. Current scope/revocation still applies; original-parent/revision constraints remain. Round-2 replay defect is repaired. |
| #46 AC10–11 | Effect, origin and protected attribution audit commit together. Legacy origin derives only from unambiguous immutable evidence; unknown origin remains unclaimable. No fabricated attribution or grants. |
| #96 AC1–3 | Explicit Export plus compatible View, filter/scope/sort confirmation, fixed operational-only columns, complete consistent sorted rows/count/as-of and durable deduplicated Pending/Running/Ready/Failed lifecycle. |
| #96 AC4–6 | Requester-only exact 24-hour download, whole-snapshot current row/facet checks, durable blocked outcomes, complete private transfer preparation and atomic artifact/digest/audit. Real worker death/restart and cross-process election tests cover recovery. |
| #96 AC7–10 | Parseable deterministic CSV with documented reversible spreadsheet escaping, truthful UI/copy warning, generation/download audit, fail-closed current authority and additive no-grant migration. Export worker admission/refresh/shutdown and snapshot fencing survive integration. |
| All six final verification criteria | See completed composed verification record below. Individual branch reports and read-only review alone are not a merged-tree runtime pass. |

## Verification environment

A **fresh owned PostgreSQL 18.6 UTF8 cluster** lives at
`.agent-state/v1-spec-18/merge-round3/postgres`, loopback port **55483**.
`cluster-identity.log` records its data directory and private test-extension path.
No copied cluster was started; the crashed historical #19 cluster and host
PostgreSQL/service configuration remain untouched.

`env.sh` records the environment, including:

```sh
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55483/freefsm_v1_integration_round3?sslmode=disable'
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55483/postgres?sslmode=disable'
GOMAXPROCS=2
GOFLAGS='-p=1 -count=1 -timeout=45m'
```

Builds, logs, screenshots and fixtures are durable. `GOTMPDIR` uses the evidence
root's `tmp`; `TMPDIR` uses `.agent-state/v1-spec-18/b3` to fit Chromium's Unix
socket path limit. The new browser regression also has a Linux-only procfs alias
to its owned durable working directory; it does not relocate files to `/tmp`.
Chromium uses `/usr/bin/chromium-browser`, Firefox `/usr/bin/firefox`, and the
preserved durable browser tooling. `MAKEFLAGS` places nested builds in the
evidence root's `dist`.

## Composed execution record

### Full uncached race suite — PASS

From the integration worktree:

```sh
source /home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-round3/env.sh
BROWSER_TESTS=0 make test
```

`make test` executes `go test -v -race ./...`; the inherited `-count=1` disables
test-result caching. `full-race-final.log` and `full-race-final.exit` record exit
**0**, all 17 tested packages passing, with explicit database and backup-admin
integration enabled. Representative durations: backup **387.668s**, handlers
**610.381s**, database **54.485s**, services **32.204s**, statusflow **23.244s**.
The default browser skips are accounted for by the separate aggregate below.

### Composed browser/API/accessibility gate

**PASS**, exit 0, recorded by `browser-composed-final.log` and
`browser-composed-final.exit`. Actual invocation:

```sh
bash /home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-round3/browser-final.sh
```

That retained script sources `env.sh`, sets `BROWSER_TESTS=1` and
`ACCESSIBILITY_TESTS=1`, supplies the Chromium/Firefox/geckodriver/tooling paths,
and runs:

```sh
go test -race ./internal/handlers ./cmd/freefsm ./internal/templates \
  -run 'PersistedBrowser$|^TestDomainRepair|^TestRecoveryPersistedChromium$|^TestNavigation|^TestWorkflow|^TestInstallationBrowserRecoveryAndCoreWorkflows$|^TestHostRecoveryAfterReviewedCutover$|^TestBackupBrowserWorkflow$|^TestInteractionRecoveryNoticeBrowser$|^TestDocumentLineEnterBrowser$|^TestCustomerExport|^TestAssetIdentityAPIContext$|^TestProjectAssignedContextIsClosedHTTPAndAPI$|^TestSessionReferenceHarnessHTTP$|^TestFilePublication.*HTTP$' \
  -count=1 -timeout=30m -v
```

All three packages passed: handlers **470.079s**, installed executable tests
**11.820s**, templates **62.376s**. This includes session expiration/recovery,
retained-global fields, File publication/client replay/legacy Office, Project,
Asset, durable CSV, the new domain repairs, and prior installed product journeys.
The corrected typed-field browser passed in **18.77s**; its independent focused
race run also passed before the aggregate (`browser-repair-focused.log`).

The full race suite covers all production/Go test repairs; the only subsequent
test change was the CJS browser assertion/journey, exercised by this final
aggregate. No production code changed after the successful full suite.

`accessibility-final/summary.json` records Chromium **154.0.8037.97**, Firefox
**157.0.1**, **35 pages**, **510 measurements**, **128 interaction states**,
**5,549 text measurements**, and **zero violations**, including both engines'
reduced-motion checks. Screenshots and detailed reports remain in that directory.
This is the existing measured route set, not a new claim to certify every newly
introduced route or the full branded/physical-device matrix.

### Generation, quality, execution accounting and shutdown

After the final browser aggregate:

```sh
source /home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-round3/env.sh
make generate lint compile
git diff --check
```

All passed (`quality-final.log`, `diff-check-final.log`); templ reported
`updates=0` and Ent generation introduced no further changes.
`checks-summary.json`, produced by `summarize-checks.py`, validates both exit
files, absence of failed tests, required browser passes, zero accessibility
violations and log hashes. Of **39** full-suite top-level skips, **33** execute
and pass in the final browser aggregate. The remaining six are:

- `TestStartupRejectsUnsupportedServerBeforeBootstrap`: PostgreSQL <16 fixture
  is not supplied by the PostgreSQL 18 cluster.
- `TestBackupBrowserNginxScale`: optional scale/proxy gate not enabled.
- `TestAuthorizationRestartProbe`, `TestBackupCrashChild`,
  `TestAssetIdentityRestartProbe`, `TestCustomerExportWorkerProcess`: standalone
  subprocess entry points; their owning parent tests run. The export helper is
  the **one selected skip** in the final aggregate; its real crash-recovery
  parent passes in that same aggregate. It is not reported as a direct pass.

The backup admin URL was set: backup/recovery integration was executed, not
skipped. The optional 1-GiB mode was not enabled.

`original-final.json` verifies **842 recorded original-checkout file hashes** and
the unchanged dirty status (`MM CONTEXT.md`, untracked `.agents/` and
`skills-lock.json`). Before shutdown, `cluster-prestop.log` reconfirms the owned
data directory and no remaining test clients. `cluster-stop.log` records clean
shutdown and `cluster-control-final.log` reports **shut down**. The durable
cluster and evidence are retained; archived clusters and host services are intact.

### Explicit unsuccessful attempts

- The first full-suite attempt (`full-race.log`) was interrupted by the outer
  tool timeout before completion. It is neither a passing full run nor evidence
  of a product test failure. The detached final runner records exit status files.
- Repair red runs intentionally failed before corrections. Initial test-fixture
  compilation and Chromium socket-length errors are preserved separately in
  `repair-result.md`; successful runtime evidence uses the corrected fixture and
  durable shorter path.
- `browser-composed.log` completed but failed the inherited typed-field journey's
  expectation that inline Project creation persists progress 37. Canonical #32
  requires zero-at-creation. The repaired journey verifies zero, then explicitly
  edits progress to 37 and reloads; it retains required-field error/input and
  persisted custom-field/description checks. The failed aggregate is not credited
  as a pass, even though its other journeys and accessibility checks succeeded.

## Residual boundaries and closure policy

These are scoped slices, not whole-v1 release acceptance. #23 supplies the required
executable recovery reference, not a production offline client. #30's general
immediate-PATCH/API cutover remains #41. #46 supplies available-parent File
primitives; Lease Files (#65), additional Delivery snapshots (#56), and general
sync/reconciliation remain their owning downstream work. Asset association
timelines (#35), Service Entries and Lease history are not invented by #34.

Native Linux/FreeBSD service/reboot/release matrices, branded current/previous
browser and physical-device certification, unsupported PostgreSQL startup fixture,
and optional nginx/1-GiB scale are not established by this round. #24's existing
device-matrix gap remains open regardless of local browser/accessibility success.

**Scoped acceptance is supported for all six merged slices.** #23, #30, #32, #34
and #96 are closure candidates on the recorded ticket criteria. #46 is a scoped
closure candidate under the established available-parent/downstream ownership
contract, with its literal AC1/AC6/AC9 still partial for Lease and additional
Delivery snapshots. An unconditional claim that every #46 bullet is complete
would be inaccurate; closure must preserve those assigned downstream criteria.
No tracker state has been changed.

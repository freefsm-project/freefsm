# #106 verification feasibility and architecture review

Read-only preflight, 2026-11-26. Verified HEAD is requested base `623a8655fe44502db4078da79653e5bb3de67c1d`. Read local AGENTS, GitHub #106, Makefile, relevant source and the #101 evidence record. No runtime started, no tests/build/generation executed, no Git or GitHub mutations. This document is a proposed qualification procedure, **not acceptance evidence**. The separate acceptance-matrix owner covers requirement-by-requirement scope.

## Readiness and external dependencies

- GitHub native dependency lookup reports #30, #64 and #73 closed. This does not itself prove their implementation meets #106.
- Available on PATH: Go, PostgreSQL server/client tools, Node/npm, Chromium wrapper, Firefox, Podman. Observed Go `1.26.11-X:nodwarf5`, PostgreSQL `18.6`. `ent` and `templ` exist at `/home/adam/go/bin`; Makefile adds that directory for generation.
- `/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules` exists. Browser launch, module resolution, tool-version compatibility, extension creation, container availability, Secret Service access and race/C compiler readiness were **not tested**.
- `/home/adam/.cache` has approximately 605 GiB available; `/tmp` is a 16 GiB tmpfs. Use durable disk for databases, artifacts and large scratch, not `/tmp`.
- #101's authoritative durable directory exists at `/home/adam/.cache/freefsm-issue101-evidence-20261126`. Its `env.sh` was inspected for prerequisite names only; **do not source it**: it points at #101's retained databases, ports and output files.
- No #106 DB, app role, administrator, browser-tool installation, evidence root or port reservation has been provisioned. These are pending runtime prerequisites, not established external failures. No actual blocker to provisioning has been established.
- All-opt-in regression requires an unlocked usable desktop Secret Service for protected-key tests; do not stop or reconfigure that shared desktop service. Native FreeBSD/reboot and branded-browser release matrices from older documents are not prerequisites invented for this ticket.

## Safe isolated runtime and durable evidence

Create a fresh, nonexisting directory such as `/home/adam/.cache/freefsm-issue106-evidence-<UTC-run-id>` with mode 0700, and a distinct sibling scratch directory. Record their resolved paths in the repository evidence handoff. Retain `env.sh` (0600), `commands/`, `logs/`, `results/`, source manifests, browser artifacts, `build/`, `pgdata/`, `socket/`, extension fixtures and a shutdown inventory. Put `TMPDIR` on the disk scratch. Never reuse #99/#101 roots, roles, ports, databases or containers.

Provision only a new loopback PostgreSQL 18 cluster. Choose and check unused app/PG ports immediately before launch; record actual ports and owned PIDs. The following is a setup template, not an executed command; `$E` is the newly created evidence root and `$PGPORT` the checked port:

```sh
initdb -D "$E/pgdata" -U issue106_admin -A trust --no-locale -E UTF8
pg_ctl -D "$E/pgdata" -l "$E/logs/postgres.log" \
  -o "-h 127.0.0.1 -p $PGPORT -k $E/socket" -w start
psql -h 127.0.0.1 -p "$PGPORT" -U issue106_admin -d postgres \
  -v ON_ERROR_STOP=1 -c 'CREATE ROLE issue106_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION'
createdb -h 127.0.0.1 -p "$PGPORT" -U issue106_admin -O issue106_app freefsm_issue106
```

Trust authentication is confined to this disposable local cluster; keep its socket directory private. Record matching `postgres`, `pg_dump`, `pg_restore` versions. Supply application-role URL as `FREEFSM_TEST_DATABASE_URL`; supply separate same-cluster administrator URL as both `FREEFSM_INSTALLATION_TEST_ADMIN_URL` and `FREEFSM_BACKUP_TEST_ADMIN_URL`. Export fixtures explicitly need the administrator for schema-scoped out-of-band MVCC edits; never promote the HTTP application's role.

The full suite requires `backup_preflight_fixture` availability. On PostgreSQL 18, copy the fixture files from `internal/backup/testdata` to an isolated extension directory and configure **only this new cluster's** `extension_control_path` with that directory plus `$system`. Verify CREATE/DROP as the application role. Do not modify host extension files. Record the exact setup and smoke result.

Use existing purpose-built Go fixtures and browser scripts directly. **Never revive the retired generalized harness.** Tests already create real PostgreSQL schemas, sessions, `httptest.Server` listeners and browser processes. A persistent manual acceptance server, if needed, must use a separate #106 database, explicit `FREEFSM_ADDR=127.0.0.1:<checked-port>`, explicit DB settings and separate `FREEFSM_UPLOAD_DIR`/`FREEFSM_STATE_DIR`; no host service install and no default application DB. Keep SMTP/maps unset or disposable.

Browser prerequisites:

- `BROWSER_TESTS=1`; `BROWSER_EXECUTABLE` must point to a verified executable (the #101 Chromium cache path is a reference, not a readiness claim).
- Node resolution through isolated `NODE_PATH`. Checked-in template browser package pins `playwright-core` 1.58.2. For shared accessibility additionally resolve `selenium-webdriver` and `axe-core`, provide `GECKODRIVER_PATH` and `FIREFOX_EXECUTABLE`, and use the existing Firefox `--allow-system-access` driver path.
- Record installed dependency versions and browser smoke results before long suites. Existing dependencies may be read without modifying another checkout; install missing dependencies only into #106 tooling storage.
- All-opt-in scale needs rootless Podman and the nginx image; keep full 1 GiB size. `ACCESSIBILITY_OUTPUT` and `FREEFSM_BACKUP_BROWSER_REPORT` must point into #106 evidence, never #101.

## Exact qualification commands

Run from this worktree, sequentially after implementation settles. Populate the above environment explicitly. Capture each expanded command, UTC start/end, numeric exit, full combined log, pass/fail/skip counts and before/after source hashes. Use `pipefail` if piping to `tee`. Freeze source after generation; concurrent source edits invalidate final-source attribution.

```sh
GOFLAGS='-p=2' make BUILD_DIR="$E/build" build
GOFLAGS='-p=2' make lint
```

`make build` runs vocabulary, ent and templ generation; inspect generated differences before the freeze. `make lint` is `go vet ./...`, not a formatting check. Also run strict templ freshness (`PATH="$HOME/go/bin:$PATH" templ generate -check`), inspect `gofmt -l` on Go source, and `git diff --check`; these are distinct gates.

Minimum issue-required real-DB repository test command:

```sh
FREEFSM_TEST_DATABASE_URL="$ISSUE106_DATABASE_URL" \
FREEFSM_INSTALLATION_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
FREEFSM_BACKUP_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1 -count=1 -timeout=180m' make test
```

This is race-enabled (`go test -v -race ./...`). Unset browser/other opt-ins still skip their coverage; this minimum command alone cannot establish ticket browser acceptance. Prefer #101's qualified full-regression pattern:

```sh
FREEFSM_TEST_DATABASE_URL="$ISSUE106_DATABASE_URL" \
FREEFSM_INSTALLATION_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
FREEFSM_BACKUP_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
BROWSER_TESTS=1 ACCESSIBILITY_TESTS=1 FREEFSM_OFFLINE_KEY_TESTS=1 \
FREEFSM_BACKUP_BROWSER_SCALE=1 FREEFSM_BACKUP_TEST_GB=1 \
CGO_ENABLED=1 GOMAXPROCS=2 \
GOFLAGS='-p=1 -count=1 -timeout=180m -skip=^TestBackupBrowserNginxScale$' make test
```

Exactly the memory-budget scale test is excluded from race in this documented pattern, with mandatory same-frozen-source non-race companion:

```sh
FREEFSM_TEST_DATABASE_URL="$ISSUE106_DATABASE_URL" \
FREEFSM_BACKUP_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
FREEFSM_BACKUP_BROWSER_SCALE=1 GOMAXPROCS=2 GOFLAGS='-p=1' \
go test ./internal/handlers -run '^TestBackupBrowserNginxScale$' -count=1 -v -timeout=30m
```

Leave `FREEFSM_BACKUP_BROWSER_BYTES` and `FREEFSM_INSTALLATION_BINARY` unset. #101 demonstrated the race instrumented scale exceeded the unchanged 768 MiB budget; do not call the split run “every test passed under race”. Record any opt-ins deliberately not exercised as gaps.

Existing targeted architecture regressions, with the same explicit DB/admin/tool environment:

```sh
go test -race ./internal/services -run '^TestSearchTechScopeFindsAssignedResultBeyondBroadLimit$' -count=1 -v
BROWSER_TESTS=1 go test -race ./internal/handlers -run '^(TestCustomerMastersPersistedBrowser|TestAccessibleInteractionPersistedBrowser)$' -count=1 -v -timeout=15m
go test -race ./internal/api/v1 -count=1 -v -timeout=30m
```

These are existing regressions, **not complete #106 acceptance**. New ticket-specific persisted search/list-state/browser and HTTP/API tests still need implementation and exact named invocations. Avoid regex-only acceptance with zero matched tests. Existing `customer_masters_integration_test.go` demonstrates real DB + sessions + production router + CSRF + served static assets; mock-only template tests cannot prove persisted behavior. API authorization should exercise bearer/pairing and existing field boundaries where applicable, not silently create a new mobile global-search feature.

For an all-opt-in repository result, reconcile unsupported-server main-suite skip with a separate disposable PostgreSQL 15 negative run (not the full suite):

```sh
FREEFSM_TEST_DATABASE_URL="$ISSUE106_PG15_DATABASE_URL" \
FREEFSM_INSTALLATION_TEST_ADMIN_URL="$ISSUE106_PG15_ADMIN_URL" \
FREEFSM_BACKUP_TEST_ADMIN_URL='' GOMAXPROCS=2 GOFLAGS='-p=1' \
go test -race ./cmd/freefsm -run '^TestStartupRejectsUnsupportedServerBeforeBootstrap$' -count=1 -timeout=15m -v
```

Reconcile subprocess-helper direct-entry skips against actual passing parent/subprocess evidence. Interrupted runs, compile failures, unexecuted tests and opt-in skips are never passes. Preserve failed attempts, not just successful reruns. #101's old `/tmp` evidence was lost; only fresh durable evidence can qualify this ticket.

## Architecture risks to resolve in the plan

1. **Authorization before matching/ranking/count/pagination.** `services/search.go` returns five separately limited, unordered result slices; handler fixes limit at 10. It also retains role-string legacy paths. Installed navigation reads use current principals and SQL traversal predicates (`authorization/navigation_reads.go`), with tenant, assigned, archive and conversion-hidden restrictions. A combined SQL implementation bypasses ent interceptors unless it deliberately reproduces the authoritative boundary. Do not fetch broad rows and filter after rank/limit. Tests need installed middleware and narrow current grants, not only the legacy role fixture.
2. **Facet predicates are security-sensitive, not just output redaction.** Searching sensitive Item/Lease/Customer values and then removing labels still leaks through presence/rank/count. Build authorized searchable terms first. Related-record matches must carry explicit authorized match provenance without giving access to unrelated children. `FindCustomers` already independently gates active Contact/Location SQL matching, returns one deterministic relationship label and redacts financial fields; retain this behavior.
3. **Fail as a whole.** Current global search propagates primary query failures but ignores Customer/status enrichment errors (`search.go:161,167`, and legacy equivalents). Handler returns raw `err.Error()` as HTTP 500. New combined projection must propagate all required enrichment/iteration errors and render safe failure with filters retained. Distinguish denied facet (not queried) from failed authorized query; never turn failure into zero results.
4. **Stable semantics at scale.** Five per-type prelimits cannot establish global exact/prefix/elsewhere order. Define canonical type/identity ties and count from the same authorized predicate; prevent duplicate Item/Pricebook identities. Use literal case-insensitive matching (beware SQL `%`/`_` wildcard semantics), bounded validated page offsets and parameterized filters. Active scalar custom fields require definition/type-aware matching rather than searching raw JSON, which would include retired values and non-scalar data.
5. **Preserve effective domain projections.** Current search resolves `invoice_effective_status`; replacing it with raw stored status regresses settled/archive behavior. Lease workspace already has domain-specific filters and independent Billing. Reuse authority/projections, not the old five-slice service's semantics as a universal abstraction.
6. **List-state ownership and strict parsing.** Customer list presently normalizes malformed page/per-page/sort silently; Lease list uses user-keyed cookies, URL precedence, constrained relationships and reauthorization on restore (`handlers/leases.go:129–187`). Shared state must specify URL-key presence versus empty values, Reset scope, per-user/list key, duplicate/unknown/malformed parameters and unavailable grants. Cookies remain untrusted input. Clarify whether last-used state must cross devices; current Lease cookie does not establish that guarantee.
7. **History and failure races.** `interaction.js` already disables HTMX snapshot resurrection, reloads popstate/bfcache under current authority and stores URL/scroll only. Preserve this security behavior. Do not store response bodies or introduce a competing history owner. Existing failed-refresh behavior hides prior results; #106's explicitly-labelled prior-result requirement needs an intentional integration contract, particularly when permission loss demands removal. Exercise out-of-order responses, actual Back/Forward, revocation between visits, changed page membership, storage disabled and same-browser user switching. Wait for HTMX settle in browser tests: #101 recorded native-GET-versus-HTMX timing false failures.
8. **Concurrency and migration proof.** Consistent count/page projections and authorization changes during reads need an explicit policy; pagination should not leak through stale cross-request caches. Any durable preference writes need current actor binding and applicable transaction/audit rules, without manufacturing a domain mutation for read-only search. Migration fixtures must preserve existing custom-field history, protected conversion provenance and nonprotected grants.

## Shutdown and evidence acceptance

Inventory owned app/browser/driver/test/container PIDs and ports at start. At completion, stop only #106-owned processes; stop the dedicated PG cluster with `pg_ctl -D "$E/pgdata" -m fast -w stop`. Stop any dedicated PG15/nginx containers by recorded ID; do not broadly kill browsers/PostgreSQL or stop shared desktop services.

Record `pg_ctl status`, absence of owned live PIDs/postmaster PID, closed owned TCP listeners, exited/removed owned containers and no remaining test/browser children. Keep data directories, logs, manifests, database-version markers and failed-run artifacts. If fixture cleanup drops test schemas, preserve required dumps/assertion artifacts before cleanup; cluster retention alone does not retain every test's data.

Final handoff must name actual evidence paths and latest frozen-source results, list failures/skips/limitations, and explicitly confirm shutdown from observations. No build, lint, real-DB, browser, API, concurrency or shutdown acceptance has been executed by this preflight.

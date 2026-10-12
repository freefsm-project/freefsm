# v1 integration round 2: tickets 21, 22, 24, 27, 28, 30, 31 and 46

Date: 2026-10-12

This document records the scoped acceptance position for the round-2 integration
based on the pinned ticket evidence and source review. It is deliberately not a
claim of full ticket acceptance, whole-v1 completion, or closure of downstream
work. A **Pass** is bounded by the text in its row. A **Partial** or **Fail** row
identifies an open requirement; several rows can be manifestations of one root
gap.

## Integrated ancestry

The integration started at `521de47` and preserves these exact tips:

| Ticket | Exact tip | Integration decision |
|---|---|---|
| #21 | `adc0398e9378199251463f5600dda1a569171aa4` | Merged through `9de3cf0`. |
| #22 | `7ddfa78e1e08972e4dc7a307179f2787de4f58e1` | Already an ancestor of #21; not replayed as an independent patch. |
| #24 | `d2e6870bf6df17d7f21f199c7dd2c85c5744618f` | Merged through `289216e`. |
| #27 | `5d26335743ad38108fb24d8a5651ac89006b2543` | Merged through `c29c4c4`. |
| #28 | `aa626c42a4b70201ec92b2c55d6849fde30b86ae` | Merged through `b5fbd3f`. |
| #30 | `468830cb4238d050c87acef0eb024b88a6627d1d` | Merged through `8a539f1`. |
| #31 | `bfe3188d999ac0cd597219af7f2e81e178f73356` | Merged through `df0d572`. |
| #46 | `f5b00d265a119300d6379135546f1641c9ee9a7a` | Merged through `0d1ae73`. |

The post-merge repair `b8d6df9` keeps active, statusless Estimates eligible for
the conversion preview. This preserves #30's preview flow without incorrectly
making conversion depend on a Status definition.

### Conflict and composition decisions

- The #24 session conflicts retain both #21/#22 credential errors and #24's
  conclusive `ErrInvalidSession` distinction. Invalid credentials are discarded;
  database/dependency failures and failed forced-password checks return the
  shared unavailable response instead of failing open.
- The #30 Estimate conflict retains #24's effective `detailCan("convert")`
  permission gate and #30's reviewed conversion-preview link. Generated templ
  output follows the `.templ` source.
- The #31 navigation conflict keeps #24's effective-grant navigation abstraction
  and adds Contacts and Locations to `operationalLinks`; it does not restore
  Role-name checks or unredacted Customer navigation.
- The #46 dependency conflict keeps the structural CFB parser and the integrated
  crypto/network versions. The File widget keeps capture-phase queue submission
  and also cancels HTMX requests, preventing two writers/publication identities
  for one click.
- The #24/#27 Status composition retains ID-aware workflow intent and current-
  grant discovery together with same-target no-change receipts. The #24/#28
  Asset composition distinguishes omitted Status (retain) from explicit No
  Status (clear). Repeated 070/071 migration prefixes remain separate full
  filenames and were not renumbered.
- The obsolete backup browser assertion was replaced with behavior: activate the
  accessible **Mail** link, verify the independent Mail settings page and SMTP
  control, then use browser Back and verify restoration of the prior Settings
  section. It no longer selects the removed Email tab markup.
- The legacy Office browser regression was repaired test-first to match receipt
  reconciliation rather than replay. It now requires three persisted Files,
  three distinct publication POST identities and no fourth POST after a lost
  response. A DOC body submitted as XLS must return HTTP 400 and must not create
  a fourth File. The focused repaired journey and the final aggregate both pass.

## Criterion matrix

### #21 — invitation and manual onboarding

| AC | Status | Scoped evidence or boundary |
|---|---|---|
| 1. Invitation purpose, exact 72 hours, one-time activation | Pass | Distinct invitation proof, exact lifetime, policy-valid chosen password and one-time activation use production transactions and browser evidence. |
| 2. Resend, expiry, concurrency and invalid-proof denial | Pass | Replacement invalidates the prior proof; expiry, wrong purpose and replay deny; concurrent redemption has one winner. |
| 3. Known unavailable and uncertain email delivery | Pass | Known unavailability stops before account creation; failed/uncertain SMTP retains one inactive pending account with truthful resend/verification guidance. |
| 4. Temporary manual credential | Pass | Role changes, guessed browser routes and API login cannot bypass replacement of the temporary password. |
| 5. Shared password policy and matching UI | Pass | The composed #22 policy is loaded inside onboarding transactions, including configured minima and the bcrypt byte boundary. |
| 6. Sealed authority and races | Pass | Current session, Role, sealed authority and pending state are rechecked under the shared fence. |
| 7. Legacy onboarding evidence | Pass | Historical users, hashes, sessions and facts survive; uncertain recovery proof is quarantined rather than repurposed. |
| 8. Failure atomicity and secrecy | Pass | Validation leaves proof usable; activation/audit failure rolls back; bearer values are excluded from UI, logs, audit and referrers. |
| 9. Independent grants and atomic audit | Pass | Creation, resend and redemption bind the effect and protected audit in the committing transaction. |
| 10. Non-destructive migration | Pass | Migrations preserve evidence and invent neither activation history nor ordinary-Role grants. |
| 11. Required evidence | Pass on ticket evidence; fresh composed run below | Reported explicit-PostgreSQL/backup race, denial, upgrade, concurrency, SMTP and Chromium coverage supports the slice. |

Boundary: this does not claim #23 browser/mobile session lifecycle or native
release/reboot proof.

### #22 — password policy and account recovery

| AC | Status | Scoped evidence or boundary |
|---|---|---|
| 1. Sealed default policy and prospective changes | Pass | Setup, invitation/manual onboarding, self/Admin reset, public recovery and host recovery share one prospective policy. |
| 2. One-hour newest-only recovery | Pass | Exact expiry, purpose, replacement, concurrent one-winner redemption, replay denial and post-change invalidation are covered. |
| 3. Nondisclosure and truthful outage | Pass | Present, absent, disabled and pending requests share a nondisclosing durable public path; sealed Admin help remains available. |
| 4. Session revocation and temporary Admin reset | Pass | Email/Admin/host reset revokes all sessions; self-change preserves only the acting browser; Admin credentials remain temporary. |
| 5. bcrypt, cookie, CSRF and bypass denial | Pass | Production cookie/CSRF behavior, forced-state denial, invalid-proof denial and no-referrer/no-store bearer pages are covered. |
| 6. Host-local recovery | Pass | It requires host access, an existing enabled protected Administrator, policy, forced change, revocation and host audit; setup cannot reopen it. |
| 7. Concurrent security changes and atomicity | Pass | Reset/change/disable/replacement/login races and audit/storage faults preserve the prior usable state without stale proof or session survival. |
| 8. Legacy identities and proof | Pass | Identities and hashes survive; uncertain legacy proof remains review evidence and requires safe replacement. |
| 9. Independent grants and atomic audit | Pass | Current authority, session and policy are checked under the shared fence with the credential effect and protected audit. |
| 10. Preservation | Pass | Full-name migrations coexist without rewriting facts or adding ordinary-Role grants. |
| 11. Required evidence | Pass on ticket evidence; fresh composed run below | Reported explicit-PostgreSQL/backup race, SMTP/browser, host executable, upgrade, denial and concurrency coverage supports the slice. |

#22 is integrated through #21 ancestry. This does not claim #23 session
lifecycle, native release verification, or unrelated operational configuration.

### #24 — accessible navigation and form interaction

| AC | Status | Scoped evidence or boundary |
|---|---|---|
| 1. Current permission navigation and Back restoration | Pass for installed routes | Effective-grant navigation, current section, filtered counts, grant-loss refresh and filter/sort/page/scroll restoration have persisted coverage. |
| 2. 320 px, native 200%, orientations and workflows | Pass for exercised workflows | Ticket evidence reports 35 routes and 510 measurements across both themes/orientations and fine/coarse layouts. |
| 3. Keyboard, focus, validation and non-drag controls | Pass for exercised workflows | Menu/dialog focus, linked summaries, retained input, map keys, non-drag alternatives and line-editor behavior are covered. |
| 4. Contrast, non-color meaning, targets, reduced motion | Pass for measured states | Ticket measurements report compliant text/control contrast, 24/44 px targets and reduced-motion behavior. |
| 5. Theme preference/default/migration | Pass | System default, per-user Light/Dark/System persistence and legacy preservation are covered. |
| 6. Save, dirty guards and confirmations | Pass for installed forms | Explicit Save, Stay/Discard, Back/closure guards, validation focus and scoped consequential confirmations are present. |
| 7. Unknown outcomes, receipts and conflicts | Pass for installed adapters | Durable action identities, reconciliation-before-retry, authorized review, restart/fault/race handling and audit rollback are covered. |
| 8. Loading/updating/empty/no-match/failure | Pass | Previous-results labeling, exact Retry URLs, persistent unknown/error state, no-match distinction and authority-loss sanitization are covered. |
| 9. Required browser/device matrix | **Partial — open** | Exercised: Chromium 154 and Firefox 157. Missing: current/previous branded Chrome and Edge, previous Firefox, current/previous Safari on macOS, real iOS Safari and Android Chrome. Deferral to #114 is not a pass. |
| 10. Grants, commit invariants and audit | Pass for new owning adapters | Current authority and effect/audit/receipt composition are supported without claiming every historical mutator was rewritten. |
| 11. Non-destructive migration | Pass | Additive 070/071 facts and preference/upgrade fixtures add no grants or rewritten history. |
| 12. Required evidence | Pass with disclosed platform gap; fresh composed run below | Generation, lint, compile, explicit database/backup and selected race-enabled browser/API evidence supports the installed slice; AC9 remains open. |

The sole established #24 gap is the literal branded browser and physical-device
matrix in AC9, not an additional installed-app defect.

### #27 — semantic workflow configuration

| AC | Status | Scoped evidence or boundary |
|---|---|---|
| 1. Fixed category sets | Pass | Closed Job/Project/Estimate categories, constraints and ordering are retained. |
| 2. Status presentation, defaults and semantic moves | Pass | Label/order/default constraints, identity, cross-category meaning revisions including ABA, and protected audit are covered. |
| 3. Safe deletion/replacement | Pass | Same-category replacement covers active, archived and hidden references atomically; default/last/race restrictions are enforced. |
| 4. Future-only defaults and identity/history | Pass | Default, rename and reorder preserve existing identity/state without fabricated transitions. |
| 5. Fixed Invoice slots and settlement precedence | Pass | Six slots/order and derived/manual restrictions remain; settlement uses semantic category rather than label. |
| 6. Reviewed legacy inventory | Pass | Definitions and all reference classes are inventoried; explicit mapping is required; pre-013/pre-032 one-shot upgrades are covered. |
| 7. Configuration races and semantic reporting | Pass | Shared fences/locks reject or commit wholly; dependent Project behavior uses category. |
| 8. API/offline intent | Pass | Rename-safe record/meaning revisions, stale/ABA denial, explicit reselection and excluded definition routes are covered. |
| 9. Independent grants and atomic audit | Pass | Discovery uses current parent View in one snapshot; mutation separately requires Status permission and commits with audit. |
| 10. Preservation | Pass | Reviewed upgrades retain IDs, relationships and evidence and add no ordinary-Role grants. |
| 11. Required evidence | Pass on ticket evidence; fresh composed run below | Reported lint, explicit PostgreSQL/backup race and focused HTTP/API/browser evidence supports the slice. |

This does not claim full Job detail/list authorization, general synchronization,
Delivery lifecycle, or later Project/commercial slices.

### #28 — Tags and Asset vocabulary

| AC | Status | Scoped evidence or boundary |
|---|---|---|
| 1. Normalized uniqueness and concurrency | Pass | Unicode case/outer-space keys and database constraints cover company Tags and separate Type/Status lists. |
| 2. Independent definition and link controls | Pass | Definition View/Manage and parent-bounded Tag Attach/Detach remain independent. |
| 3. Retirement/reactivation and reserved names | Pass | Retirement blocks new selection but preserves references; the same stable identity can reactivate and its text remains reserved. |
| 4. Zero-reference deletion and races | Pass | Restrictive references/locks include archived records and prevent orphaning/history loss. |
| 5. Rename/recolor/audit and descriptive Status | Pass | Definition audit and current display follow definitions; Asset Status has no operational lifecycle effect. |
| 6. Retired required Type | Pass | Unrelated edits retain it; replacement must be active and cannot implicitly reactivate the old definition. |
| 7. Legacy preflight | Pass | Collision, invalid/unmapped and orphan conditions stop without inferred merge or cleanup. |
| 8. Existing Tag parents | Pass | Six supported parents retain bounded authorization; future domains remain downstream. |
| 9. Independent grants and atomic audit | Pass | Definition/link effects and Asset Type/Status selection commit with protected audit. |
| 10. Preservation | Pass | Additive migrations preserve IDs, spelling and references without adding grants. |
| 11. Required evidence | Pass on ticket evidence; fresh composed run below | Reported lint, explicit PostgreSQL/backup race, focused database/HTTP and Chromium journeys support the slice. |

Omitted Status retains the current reference; explicit zero clears it; malformed
input denies; revocation or audit failure rolls back. Asset ownership remains #34.

### #30 — Custom Field evolution and conversion recovery

| AC | Status | Scoped evidence or exact gap |
|---|---|---|
| 1. Retirement, history, requiredness and options | Pass on stable-identity interpretation | Retirement/history/input removal and requiredness suspension work. Retired option text stays reserved; reactivating the same retained identity is not identity reuse. |
| 2. Reactivation and cosmetic changes | Pass | Applicable validation resumes without backfill, automatic replay/default or missing-value blockage; cosmetic changes preserve valid revisions. |
| 3. Dependency checks and races | Pass for supported definitions | Stored, archived/hidden, pair and active-cycle references constrain mutation under the shared fence. Retained globals are the AC4/5/7/10 root gap below. |
| 4. Compatible one-to-one pairing | **Partial — retained-global gap** | Company-owned definitions enforce key/type/precision/option compatibility. Released `company_id IS NULL` definitions are omitted from lookup/counterpart checks. |
| 5. Captured cycle and representable reversion | **Partial — retained-global gap** | Modern cycles capture definitions, pairs, options and precision. Capture is company-equality scoped and omits retained globals. |
| 6. Preview, defaults, authority and exemptions | Pass | Required Invoice preview, paired precedence, unpaired defaults, Invoice Edit+Convert and captured-cycle reversion are covered. |
| 7. Historical evidence/review | **Fail for retained-global data** | Post-042 released data can contain NULL-company definitions. Guards cannot match them and explicit review requires company equality, making affected cycles unreviewable/unrecoverable. |
| 8. Executable field API/offline cases | Pass for reviewed protocol | Operation IDs, per-field ABA revisions, terminal receipts, authorized comparison and new-ID recovery are covered. Immediate online compatibility is not credited with OFF-02 guarantees; #41 owns full cutover. |
| 9. Independent grants and atomic audit | **Partial — company revision leak** | Modern mutation/audit is transactional, but Job `fieldState` selects definitions without owned/global scoping and exposes foreign-company IDs/synthetic revisions. |
| 10. Preservation | **Partial — retained-global gap** | Migrations are additive and add no grants, but this historical class is not safely classified or recoverable. |
| 11. Required evidence | **Partial** | Existing evidence omits genuine post-042 retained-global upgrade/review/recovery and two-company revision nondisclosure regressions. |

There are exactly two established #30 root gaps:

1. Apply one safe owned-or-retained-global rule to pairing, capture, destructive
   guards and historical review; cover genuine post-042 NULL-company upgrade,
   denial, explicit review and recovery.
2. Scope Job field revision state to the authorized company plus intentionally
   retained globals; add an authenticated two-company nondisclosure regression.

Required-field reactivation is intentionally nonblocking, with no backfill or
automatic retry/default. This ticket does not claim general API synchronization.

### #31 — Customer, Contact and Location masters

| AC | Status | Scoped evidence or boundary |
|---|---|---|
| 1. Identity, kind and immutable child ownership | Pass | Required identity and editable kind are enforced; child ownership is not editable. |
| 2. Duplicates, optional unique primaries and no implicit grant | Pass | Duplicates remain valid; partial indexes and Customer locking protect one primary without creating Job links or authority. |
| 3. Search, archived inclusion, paging/order/state | Pass | Relationship-aware search, explicit archived inclusion, stable paging/order and query retention are covered. |
| 4. Dashboard and direct Related Work | Pass | Recent cards/counts/View all and the direct, deduplicated, nonrecursive, permission/lifecycle-filtered union are supported. |
| 5. Authorized receivables and separate Credit | Pass | Active issued/non-Void positive due, six aging buckets, hidden-record exclusion and separate Credit are covered. |
| 6. Archive/restore preservation | **Partial at ARC-02 boundary** | Contact/Location blockers, primary clearing, reviewed cascade and Customer-only restore pass. Customer-owned Asset cascade versus Company-owned placement waits for #34; Lease premises blockers wait for #65. |
| 7. Legacy identities/links and preflight | Pass | Ambiguity, duplicate primaries, ownership-history gaps and link discrepancies are reported without inference or collapse. |
| 8. Races and context isolation | Pass after `bfe3188` | Existing children use independent grants; hidden parent identity is redacted; context-only and malformed-parent paths deny; creation requires an authorized active Customer. |
| 9. Independent grants and atomic audit | Pass | Current child permission, session, parent lifecycle and locks are rechecked transactionally; audit failure/revocation rolls back. |
| 10. Preservation | Pass | Additive migration retains identities, links and legacy facts and adds no grants. |
| 11. Required evidence | Pass on bounded ticket evidence; fresh composed run below | Reported generation, lint, explicit PostgreSQL/backup race and persisted Chromium child journeys support the Customer/Contact/Location slice. |

Scoped Customer/Contact/Location acceptance is supported. Literal full ARC-02 is
not: the downstream Asset ownership/cascade boundary is #34 and Lease is #65.

### #46 — validated File upload and management

| AC | Status | Scoped evidence or exact gap |
|---|---|---|
| 1. Supported parents | **Partial — downstream** | Customer, Project, Job, Asset, Estimate and Invoice are supported. Lease remains #65; unsupported Contact/Location/Item/Time Entry were not silently added. |
| 2. Content/extension validation | Pass | Advertised formats have content checks, including structural CFB and genuine DOC/XLS/PPT fixtures plus mismatch/truncation denial. |
| 3. Limits, batch and independent progress/retry | Pass | Default/configurable 25 MiB, ten-file queue, retained successes and stable-ID retry are covered; duplicate enhanced submission is prevented. |
| 4. Validated, currently authorized publication | Pass | Staging is not visible File state; publication rechecks authority/parent, validates bytes, fsyncs and commits File plus audit atomically. |
| 5. Preview/download/rename/replacement | Pass | Grants, missing-byte failure, fixed-extension rename, duplicate display names and new replacement identity are covered. |
| 6. Removal and Delivery snapshots | **Partial — downstream** | Retained row/bytes/removal attribution and denial of ordinary access/restore pass. Delivery File attachments and immutable snapshots remain #56. |
| 7. Resumable API and offline rename/remove | **Partial — device-bound replay defect** | Transfer, chunk, digest, parent, revision, no-op and stable-publication primitives exist, but publication replay is keyed by user+operation+intent rather than originating stable API client. |
| 8. Writers, revisions and retry identity | Pass for current File writers | Web/API publication, rename/remove, authority, parent, revisions and stable identity are supported; generated PDF storage remains independently authorized. |
| 9. Migration/backup and Delivery retention | **Partial — downstream** | Legacy/removed File facts survive upgrade and encrypted backup; existing Delivery PDF survives. Future Delivery File snapshots remain unproved. |
| 10. Independent grants and atomic audit | Pass for actual effects | First effects capture current authority plus device, operation, occurrence and acceptance atomically. Cross-device replay is the AC7 defect, not a second effect missing audit. |
| 11. Preservation | Pass | Migration is additive, preserves unknown attribution and adds no grants or fabricated facts. |
| 12. Required evidence | Pass on bounded ticket evidence; fresh composed run below | Existing explicit-database race, File/browser, backup, format and API evidence omits a two-device publication replay regression. |

The actionable #46 defect is to persist the originating stable API client/session
key with publication evidence and deny same-operation publication replay from a
different client. Lease Files (#65) and Delivery File snapshots (#56) remain
downstream rather than #46 implementation defects.

## Fresh composed verification record

These are results from the integrated tree, not ticket-branch runs. PostgreSQL 18
ran in the integration-owned cluster on `127.0.0.1:55429`; the application
database was `freefsm_v1_integration_round2_owned` and the backup admin database
was `postgres`. Temp files, Go build work and durable evidence were rooted at
`/var/tmp/freefsm-v1-integration-round2.nZPsYe`.

### Ancestry and generation gates

The ancestry verification is reproducible with:

```sh
for tip in adc0398 7ddfa78 d2e6870 5d26335 aa626c4 468830c bfe3188 f5b00d2; do
  git merge-base --is-ancestor "$tip" HEAD || exit 1
done
git rev-parse HEAD
git diff --check
git status --short --branch
```

All eight checks succeeded. The integration HEAD before the final evidence commit
was `05a70caee24fe11b9fd437dc168f6c362e198257`. The source/generated diff check
passed; only this evidence document remained for the final evidence commit.

Generation, lint and compile were refreshed after the final browser regression
repair as follows:

```sh
set -o pipefail
artifact_root=/var/tmp/freefsm-v1-integration-round2.nZPsYe
TMPDIR="$artifact_root/tmp" GOTMPDIR="$artifact_root/tmp" GOMAXPROCS=2 GOFLAGS='-p=1' \
  make templ 2>&1 | tee "$artifact_root/integration-round2-templ-final2.log"
TMPDIR="$artifact_root/tmp" GOTMPDIR="$artifact_root/tmp" GOMAXPROCS=2 GOFLAGS='-p=1' \
  make lint 2>&1 | tee "$artifact_root/integration-round2-lint-final2.log"
TMPDIR="$artifact_root/tmp" GOTMPDIR="$artifact_root/tmp" GOMAXPROCS=2 GOFLAGS='-p=1' \
  make BUILD_DIR="$artifact_root/dist-final" compile 2>&1 | \
  tee "$artifact_root/integration-round2-compile-final2.log"
{ git diff --exit-code -- 'internal/templates/*_templ.go'; git diff --check; } 2>&1 | \
  tee "$artifact_root/integration-round2-generated-diff-final2.log"
```

Every invocation exited 0: templ reported `updates=0`, lint passed, compile
produced the disk-backed `freefsm` binary and generated-template/diff checks
were clean.

The coordinator additionally ran the complete `make generate` target after the
worker finished, with the same disk-backed `TMPDIR`/`GOTMPDIR`, `GOMAXPROCS=2`
and `GOFLAGS=-p=2`. Vocabulary, Ent and templ generation passed and left the
tracked tree unchanged (`git status --short` empty; `git diff --check` passed).

### Full uncached race suite

```sh
set -o pipefail
artifact_root=/var/tmp/freefsm-v1-integration-round2.nZPsYe
TMPDIR="$artifact_root/tmp" GOTMPDIR="$artifact_root/tmp" GOMAXPROCS=2 \
GOFLAGS='-p=1 -count=1 -timeout=30m' \
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55429/freefsm_v1_integration_round2_owned?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55429/postgres?sslmode=disable' \
BROWSER_TESTS=0 \
make BUILD_DIR="$artifact_root/dist" test 2>&1 | \
  tee "$artifact_root/integration-round2-full-race-owned-final.log"
```

Exit 0. This is the explicit-database, backup-admin-enabled, serial-package,
uncached full race run. It includes the corrected revoked-Administrator denial:
the current authority recheck returns 403 with no password, session, proof or
audit effect rather than the obsolete redirect expectation.

### Browser and accessibility aggregate

```sh
set -o pipefail
artifact_root=/var/tmp/freefsm-v1-integration-round2.nZPsYe
mkdir -p "$artifact_root/accessibility-final2"
TMPDIR="$artifact_root/tmp" GOTMPDIR="$artifact_root/tmp" GOMAXPROCS=2 \
GOFLAGS='-p=1' MAKEFLAGS="BUILD_DIR=$artifact_root/dist" \
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55429/freefsm_v1_integration_round2_owned?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55429/postgres?sslmode=disable' \
BROWSER_TESTS=1 ACCESSIBILITY_TESTS=1 \
BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
FIREFOX_EXECUTABLE=/usr/bin/firefox \
GECKODRIVER_PATH=/tmp/opencode/freefsm-ui-tools/geckodriver-0.36.0 \
NODE_PATH='/tmp/opencode/freefsm-ui-tools/node_modules:/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules' \
ACCESSIBILITY_OUTPUT="$artifact_root/accessibility-final2" \
go test -race ./internal/handlers ./cmd/freefsm ./internal/templates \
  -run 'PersistedBrowser$|^TestRecoveryPersistedChromium$|^TestNavigation|^TestWorkflow|^TestInstallationBrowserRecoveryAndCoreWorkflows$|^TestHostRecoveryAfterReviewedCutover$|^TestBackupBrowserWorkflow$|^TestInteractionRecoveryNoticeBrowser$|^TestDocumentLineEnterBrowser$' \
  -count=1 -timeout=20m -v 2>&1 | \
  tee "$artifact_root/integration-round2-browser-aggregate-final.log"
```

Exit 0. The aggregate explicitly includes recovery, Files, independent children,
Asset No Status, typed fields, workflow, navigation, both-engine accessibility,
guarded saves, backup and installed/host recovery. The accessibility artifact at
`accessibility-final2/summary.json` records Chromium 154.0.8037.97, Firefox
157.0.1, 35 pages, 510 measurements, 128 interaction states, 5,529 text
measurements and zero violations.

`TestBackupBrowserWorkflow` passed in 45.05 seconds, including the accessible Mail
link to `/settings/mail`, Mail heading and SMTP host control, and browser-Back
restoration. `TestFileLegacyOfficePersistedBrowser` passed in 7.79 seconds in the
aggregate. Its focused repaired run used:

```sh
set -o pipefail
artifact_root=/var/tmp/freefsm-v1-integration-round2.nZPsYe
TMPDIR="$artifact_root/tmp" GOTMPDIR="$artifact_root/tmp" GOMAXPROCS=2 GOFLAGS='-p=1' \
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55429/freefsm_v1_integration_round2_owned?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55429/postgres?sslmode=disable' \
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
NODE_PATH='/tmp/opencode/freefsm-ui-tools/node_modules:/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules' \
go test -race ./internal/handlers \
  -run '^TestFileLegacyOfficePersistedBrowser$' -count=1 -timeout=5m -v 2>&1 | \
  tee "$artifact_root/integration-round2-file-legacy-browser-green-final3.log"
```

The focused invocation exited 0. It proves three persisted Files, three distinct
publication identities with no replay POST, mismatch HTTP 400 with no persistence,
exact downloads, stable-identity rename and removal.

### Unsuccessful attempts and explicit skips

Earlier attempts are not passes. The initial port-55419 integration database
run encountered Go linker/tmpfs quota failures. Moving Go temporary files to
disk exposed the stale revoked-Administrator redirect assertion, then the
shared fixture server exhausted its storage quota during handler migrations.
Verification moved to the separately owned port-55429 cluster without restarting
or changing the shared server. The first owned-cluster run lacked the test-only
`backup_preflight_fixture` extension. After configuring the repository's fixture
extension for this owned cluster, another run encountered quota in nested
installer builds targeting worktree `dist/`. The inherited disk-backed
`BUILD_DIR` resolved that remaining environment issue. The complete successful
run above supersedes these attempts. The first browser aggregate failed the
obsolete legacy-Office retry expectation; the repaired aggregate passed.

The final DB-only full suite explicitly skipped browser opt-ins; all of those
journeys were then run in the final browser/accessibility aggregate, which has
**no selected skips**. Remaining opt-in boundaries are:

- `TestStartupRejectsUnsupportedServerBeforeBootstrap`: requires PostgreSQL <16;
  this verification used PostgreSQL 18.6.
- `TestBackupBrowserNginxScale`: separate scale/proxy opt-in, not enabled.
- Standalone `TestAuthorizationRestartProbe` and `TestBackupCrashChild`: helper
  entry points skip outside their parent-created subprocess contexts; their
  owning parent tests ran.

The optional 1-GiB backup scenario, full branded browser/real-device matrix and
native service/reboot/release matrices are not claimed. Backup-admin integration
was enabled in the successful full suite; it was not skipped for an absent URL.

### Final process and repository state

The owned cluster identity and shutdown were recorded with:

```sh
artifact_root=/var/tmp/freefsm-v1-integration-round2.nZPsYe
pg_isready -h 127.0.0.1 -p 55429 -d freefsm_v1_integration_round2_owned
psql 'postgresql://adam@127.0.0.1:55429/postgres?sslmode=disable' -Atqc \
  "SELECT version(); SELECT datname||'|'||pg_encoding_to_char(encoding) FROM pg_database WHERE datname IN ('postgres','freefsm_v1_integration_round2_owned') ORDER BY datname; SHOW data_directory; SHOW extension_control_path;"
pg_ctl -D "$artifact_root/postgres-owned" -m fast -w stop
pg_isready -h 127.0.0.1 -p 55429 -d postgres
ps -ef | rg '[p]ostgres.*postgres-owned|[g]o test|[m]ake test|[n]ode .*testdata/browser|[c]hromium.*playwright|[f]irefox.*-marionette'
```

The pre-stop check reported PostgreSQL 18.6, UTF8 application/admin databases,
the owned `/var/tmp/.../postgres-owned` data directory and the repository-local
test-extension path. `pg_ctl` stopped that cluster cleanly and the post-stop
readiness check reported `127.0.0.1:55429 - no response`. The shared cluster's
service, configuration and files were not restarted, modified or cleaned; the
earlier runs used only the explicitly created integration database and disposable
backup fixtures. Logs are
`integration-round2-owned-cluster-final-state.log`,
`integration-round2-owned-cluster-stop.log` and
`integration-round2-process-final.log`. A final process and clean-worktree audit
is also copied to the separate `worker-result.md` after the evidence commit.

The final report must preserve the open boundaries above even when all composed
verification gates pass. In particular, passing available Chromium/Firefox tests
does not satisfy #24 AC9, and tests outside the owning ticket cannot close the
#30 retained-global/company-isolation defects, #31 Asset/Lease ARC boundary, or
#46 cross-client replay and Lease/Delivery boundaries.

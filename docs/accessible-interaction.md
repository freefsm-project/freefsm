# Accessible online interaction — #24

This documents the shared frontend, its installed-parent integrations and the
available-browser evidence. The external current/previous branded desktop and
mobile release matrix remains #114; this is not a WCAG certification.

## Installed behavior

- `cmd/freefsm/static/js/interaction.js` manages keyboard/mobile navigation,
  longest-matching current destinations, dirty forms, Stay/Discard, closure
  warnings, focus restoration and linked validation summaries. Back reloads
  under current authority while restoring URL filters/sort/page and scroll.
  HTMX snapshots are disabled and old cached snapshots are removed. Failed
  refreshes hide unvalidated previous results and retain an exact-URL Retry.
- Boosted forms, enhanced `hx-boost="false"` product forms, standalone HTMX
  controls, inline fetch editors and XHR file queues share action identities and
  distinct pending, confirmed, rejected, conflict, unknown and review states.
  Native credential/onboarding forms retain their purpose-specific server flow.
- Followed redirects are reconciled through the original receipt; a final GET
  200 is not evidence of a mutation. Existing Customer operation receipts and
  File publication identities remain intact. Unknown actions are query-only.
  Only a same-context `not_received` result permits explicit submission of the
  same captured body and identity after another check. Newer local edits do not
  become the confirmed baseline of an older request.
- The original noncredential `save_history` tag travels with a pre-send identity
  and survives navigation. It is never replaced after a different sign-in.
  Missing historical receipts then require review, never blind replay. Browser
  storage contains only opaque identities/tags and navigation URL/scroll state, not
  record input, credentials, file names, files or response bodies.
  A session/permission failure removes input and replay bodies while retaining
  only unresolved opaque receipt/history metadata for later review.
- Receipt ownership does not authorize retained input. The frontend consumes
  the backend's current input-access projection and rechecks the current page,
  removing revoked facets and whole structured widgets. Explicit denial removes
  inaccessible input. A known successful transition that removes its old form
  instead navigates through its currently authorized result.
- Conflicts retain authorized proposals and current-value comparisons, including
  document lines and Job structured widgets. Backend-marked retained widgets are
  respected; legacy renderers have a bounded widget adapter. Reviewed submitters
  retain their names/values. Ordinary Save cannot advance a stale revision, and
  rejected boosted submissions keep the editor URL. Job/document status controls
  require explicit Save rather than submitting on selection.
- File upload intercepts submission before HTMX, preventing two transports from
  one click. Each file has its own action and publication identity. Unknown files
  retain their selection and cannot start a replacement batch. Reconciliation
  does not publish a premature attachment or reupload confirmed bytes.
- Job relationship lookups are15s-bounded, cancel superseded requests and reject
  non-option/error responses. Loading/failure retains an existing relationship
  and blocks incomplete Save, with a field-specific Retry.403/404 mask labels;
  401 uses shared input purging. Changing Customer immediately removes old-Customer
  choices. An existing linked Job's unrelated Save/reload is regression-tested.
- Backup create/upload persist a 32-byte capability before dispatch and recover
  lost initial responses through the engine's `action-status` protocol. Existing
  accepted/running/ready/failed/restore-complete semantics remain separate from
  application save receipts. Dismissing an unknown initiation does not unlock a
  new operation. Restore checks its existing upload/operation capability; it is
  never automatically repeated. The isolated recovery shell retains no old
  privileged form DOM.

See `docs/guarded-online-saves.md` for backend journal, transactional confirmation,
revision, authority and quarantine boundaries. Per-user System/Light/Dark and
font preferences retain the existing cross-device migration contract.

## Accessibility measurements

`internal/handlers/interaction_accessibility_test.go` serves real migrated,
authenticated routes and the independently mounted Backup router. Its browser
harness creates persisted Job/Estimate/Invoice records and white/black/yellow
workflow definitions before measuring them.

Measured on Chromium **154.0.8037.97** and Firefox **157.0.1**:

- **35 installed routes, 510 rendered measurements**, both themes; ordinary and
  populated forms, list/detail/collaboration/financial surfaces, configuration,
  workflow, vocabulary, authorization, directory, activity, schedule variants,
  Timesheets, Backup controls and the recovery shell.
- **140 deterministic interaction/validation states** include real hover, focus
  and active states for primary controls, ordinary links and current navigation,
  plus client-required and server422 error summaries in both engines/themes.
  Primary text and filled-background tokens are distinct; mouse position cannot
  silently omit these states.
- **5,481 text-node assessments**: minimum normal text **4.65:1**, large text
  **9.32:1**. Minimum measured control boundary **4.1428:1**. Configured extreme
  status colors and white-status hover are covered in both engines.
- Minimum actual target dimension **24 CSS px** for fine pointers and **44 CSS
  px** for coarse pointers. Coarse layouts include 320×640, 640×320 and a
  1280×800 touch-capable desktop; inline prose links are exempt.
- Native browser zoom **2**, DPR **2**, CSS zoom **1**, visual viewport scale
  **1**, with portrait/landscape windows. Chromium uses a test-only extension's
  `chrome.tabs.setZoom`; Firefox uses its browser `FullZoom` controller. Neither
  CSS zoom nor device-scale/viewport emulation is counted as native zoom.
- Chromium native-zoom screenshots use unclipped CDP surface capture: the normal
  Playwright screenshot clip otherwise crops physical pixels at native zoom.
- Creation/workflow dialogs are named, contained and restore focus on Escape.
  Shared tables have keyboard-contained horizontal scrolling. Short landscape
  viewports keep Save in normal flow so a sticky footer cannot cover input/errors.
- Actual Leaflet keyboard panning moves immediately under reduced motion in both
  engines: the map transform remains stable over eight animation frames after
  the key event. Map zoom/pan/fly methods and live preference changes respect the
  same preference. Dates/times identify the backend's company timezone.

These are rendered checks at the exercised states, not an assertion about every
possible data value, provider image, assistive technology or unavailable release.

## Persisted regression coverage

`TestSharedGuardedSavePersistedBrowser` covers native preference/configuration
validation and lost response, fetch Item creation, Item and structured Estimate
conflicts/reviewed correction, standalone Comment removal, exact-byte XHR upload,
opaque recovery across reload, exact same-session retry, a different real session
requiring history review, denial, and actual Role/Cost-facet revocation while an
unknown receipt is displayed. The transport fault seam discards responses only
after production handling; other cases deliberately abort before dispatch or
interrupt receipt responses.

`TestBackupInitiationPersistedBrowser` creates, downloads and validates a real
encrypted archive, drops both initial responses after acceptance, reconciles
their original capabilities, resumes the recovery shell and verifies no second
initiation or automatic Restore. The independent intercepted Backup-template
browser covers phase transitions, status failures, unknown dismissal and
restore recovery. The 32-case keyboard/serialization harness exercises the real
shared line editor; full authorized document forms are exercised separately by
the persisted handler browsers.

## Reproduction and evidence

Use an isolated UTF8 database. Final integration verification used
`postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_24_cont?sslmode=disable` and the
explicit disposable-cluster backup-admin URL. Runtime application dependencies
remain Go/static assets; browser tooling is opt-in and isolated under
`/tmp/opencode/freefsm-ui-tools` (`axe-core`, Selenium WebDriver, geckodriver0.36.0).
The original checkout's Playwright installation is consumed read-only.

```sh
export GOMAXPROCS=2 GOFLAGS=-p=2
export GOTMPDIR=/home/adam/.cache/freefsm-v1-ticket-24-buildtmp
export TMPDIR="$GOTMPDIR"
export FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_24_cont?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55419/postgres?sslmode=disable'
make templ
make lint
make test
make compile

export NODE_PATH='/tmp/opencode/freefsm-ui-tools/node_modules:/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules'
export BROWSER_EXECUTABLE=/usr/bin/chromium-browser
export GECKODRIVER_PATH=/tmp/opencode/freefsm-ui-tools/geckodriver-0.36.0
export ACCESSIBILITY_OUTPUT=/tmp/opencode/freefsm-v1-context/ticket-024-final-accessibility
BROWSER_TESTS=1 ACCESSIBILITY_TESTS=1 go test -race \
  ./internal/handlers ./cmd/freefsm ./internal/templates \
  -run 'PersistedBrowser$|^TestNavigation|^TestWorkflow|^TestInstallationBrowserRecoveryAndCoreWorkflows$|^TestDocumentLineEnterBrowser$|^TestHostRecoveryAfterReviewedCutover$|^TestBackupBrowserWorkflow$|^TestInteractionRecoveryNoticeBrowser$' \
  -count=1 -timeout=600s -v
```

Final composed verification passed after all workers handed off:

- `make templ` (zero regenerated changes), `make lint`, explicit DB/backup-enabled
  `make test` and static `make compile`. All 17 tested packages passed; handlers
  ran in 235.168s and templates in 1.056s. Unchanged packages used Go's cache,
  including the previously executed real backup suite.
- The race-enabled browser/navigation/workflow command above passed with no
  selected skips: handlers 388.702s, executable 10.929s, templates 64.791s. This
  includes real encrypted Backup initiation, installation/host recovery, the
  linked-Job failure regression, authority-loss recovery and all 32 line-editor
  cases. The final accessibility run reproduced the measurements above with zero
  violations.
- Logs: `/tmp/opencode/freefsm-v1-context/ticket-024-integration-{templ,lint,full-test,compile,browser}.log`.
  Earlier failed/outage logs remain historical failures, not passes. Scoped TDD
  evidence is retained in `ticket-024-ui-result.md` and the other worker reports.

Final measured JSON and screenshots are under
`/tmp/opencode/freefsm-v1-context/ticket-024-final-accessibility/`.

The full suite's opt-in browsers were executed separately above. The PostgreSQL
<16 negative-startup environment and nginx/1-GiB scale gate were not enabled;
standalone restart/crash helper entrypoints are invoked by their parent tests.
Native release/OS/reboot gates are not asserted by these application checks.

Unavailable #114 release entries: current/previous branded desktop Chrome and
Edge; previous desktop Firefox; current/previous desktop Safari/macOS; real iOS
Safari and Android Chrome device/release entries. Installed Chromium and Firefox
were actually tested as recorded above. No unavailable entry is counted as a pass.

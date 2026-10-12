# Accessible online interaction — ticket #24 evidence

This is a **partial implementation of #24**, not ticket acceptance or a WCAG
claim. The canonical UX-01–04/AUTH-03 requirements remain unchanged. Remaining
implementation and release-verification gaps are listed below.

## Implemented interfaces

- `static/js/interaction.js` supplies the keyboard/mobile Menu, current-section
  announcements, dialog names, dirty-form Stay/Discard (links and same-document
  Back), closure warnings, linked validation summaries, pending/error states,
  and refreshed history navigation. Browser history stores only positions and
  query URLs, not record or draft snapshots. Old HTMX snapshots are cleared;
  authenticated responses are `no-store`. BFCache returns reload under current
  authority. Unknown/error messages have no automatic fade timer.
- `static/css/interaction.css` supplies shared text/control colors, focus rings,
  coarse-pointer target sizing (including hybrid devices), contained tables,
  responsive forms/dialogs and safe-area-aware sticky form actions. Leaflet
  animations respect reduced motion. Existing click-through Job editors and
  dashboard reorder buttons retain non-drag alternatives.
- Presentation is an intrinsic Own action. Migration 058 creates a separate
  `user_presentation` table without assigning any Role grants or rewriting user
  data. No row means unknown legacy choice, presented as System. An explicit
  Light/Dark browser cookie can be imported once, atomically and audited; later
  device cookies cannot replace a saved choice. During authorization cutover or
  forced credential replacement, cookie presentation is retained and durable
  import waits. Explicit theme/font saves commit together with immutable audit.
- Customer forms carry a UUID action identity and an opaque revision of the
  displayed record. A submitted browser action must supply its identity; edits
  compare revisions while holding the existing authorization/record locks.
  Validation retains values. Conflict pages present currently authorized saved
  values alongside proposed input, require review and issue a new action ID.
- Migration 059 stores immutable, user-bound browser save receipts in the same
  transaction as the Customer effect and audit. Equal-intent replay has one
  effect; changed intent rejects. `GET /web-actions/{operation}` returns only
  operation ID, saved/unknown disposition, stable effect ID and its route, including after
  Customer grant loss. It contains no submitted values or cached representation.
  The browser queries this seam after a lost response before offering same-ID
  retry. A successful receipt navigates through current resource authorization.
- Customer lists expose sort, 25/50/100 page sizes and filters on phones, retain
  them in URLs, distinguish no records/no matches, and hide unauthorized
  Create/Edit/Archive controls. Failed enhanced list requests retain labelled
  previous results with Retry. Archive confirmation names the Customer and action.

## Persisted tests and observed red/green defects

`internal/handlers/interaction_integration_test.go` exercises the production
router with sessions, authorization, migrated PostgreSQL and real services.
`testdata/browser/interaction.cjs` adds real CSRF/static assets and Chromium.
There is no mocked save endpoint. Lost-response injection closes the connection
only **after** production handling commits a successful redirect.

Observed failures before fixes included default Light instead of System, missing
Menu semantics, absent operation IDs, missing no-match state, Back losing edits,
empty file inputs falsely becoming dirty, scroll restoration overwriting its
saved value, a missing Job dialog name, missing presentation tables in an older
hand-built regression fixture, unidentified writes bypassing replay protection,
and legacy cookies blocking authorization review. These have passing regressions.

Verified behaviors include cross-session preferences/invalid-choice atomicity,
legacy choice preservation, Customer replay/changed intent, stale-editor
comparison, validation input retention, concurrent same-ID saves, audit-failure
rollback and same-ID retry, immutable receipt protection, user-bound receipt
lookup and nondisclosure after grant loss. Persisted Chromium checks cover Menu
Escape/focus, link and Back Stay/Discard, Customer creation/reload, linked error
focus, committed response loss/reconciliation, list failure/retry, filter/sort/
page/scroll restoration, Job dialog focus, 320px form reflow in both orientations,
200% **CSS-zoom** Job reflow, and cross-device theme preference.

## Reproduction

Use a unique disposable database on the coordination PostgreSQL cluster:

```sh
export GOMAXPROCS=2 GOFLAGS=-p=2
export FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_24?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55419/postgres?sslmode=disable'
make lint
make test

NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
go test -race ./internal/handlers ./cmd/freefsm ./internal/templates \
  -run '^(TestAccessibleInteractionPersistedBrowser|TestCustomerPermissionsPersistedBrowser|TestInstallationBrowserRecoveryAndCoreWorkflows|TestDocumentLineEnterBrowser)$' \
  -count=1 -v
```

The full race suite passed with the explicit database and backup-admin URL.
The four opt-in browser regressions above passed separately. Browser evidence is
Chromium **154.0.8037.97**, not branded Chrome/Edge or a release browser matrix.
The installation test includes persisted Customer/Job creation after recovery;
the line-editor test is a separate, template-level keyboard regression.

## Remaining acceptance gaps

1. Browser action receipts, revision conflicts and authorized comparison are
   integrated with Customer detail saves only. Existing Job, document, settings,
   presentation and other legacy commands do **not** yet all provide queryable
   browser receipts/guarded revisions. Their generic lost-response message
   explicitly says checking is unavailable; this is not UX-03 completion.
2. Shared infrastructure is exercised on Customer, Preferences, Job dialogs and
   the existing installation/line-editor journeys. Complete retained-input,
   phone-card sorting, keyboard, long-form and failure coverage across **every**
   existing workflow remains unproven; existing domain-specific handlers may
   need integration changes. Legacy domain authorization boundaries documented
   by #19 remain in their owning tickets.
3. Shared contrast/target CSS is implemented, but exhaustive measured contrast
   (including customizable status colors) and all-target audits in both themes
   remain open. Native 200% browser zoom is not proved by CSS zoom.
4. Current and previous desktop Chrome, Edge, Firefox and Safari plus real iOS
   Safari/Android Chrome were not available/executed. No all-browser, screen-reader
   or all-workflow accessibility claim is made.
5. The full suite skips its opt-in browser tests (the relevant four were then
   executed explicitly), nginx-scale test, PostgreSQL <16 negative case on this
   PostgreSQL 18 host, and child-process entry points invoked by parent tests.
   The intercepted backup browser test and native release/OS/reboot matrix were
   not rerun for this UI ticket. Skips are not passes.

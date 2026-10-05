# Document-line Enter regression

## Backup browser workflow

The backup regression renders the real backup page and production HTMX, with
intercepted engine responses. It checks native `.age` browser download (no HTMX
interception), raw uploads including Unicode/whitespace archive passwords,
POST/header capability polling, safely displayed source metadata and restore
confirmation binding. It complements the real PostgreSQL handler test rather than
substituting for the 1 GB proxy acceptance exercise.

```sh
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser go test ./internal/templates -run '^TestBackupBrowserWorkflow$' -count=1 -v
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://.../postgres?sslmode=disable' go test -race ./internal/handlers -run '^TestBackupHTTPRoundTrip$' -count=1 -v
```

## Document-line setup and execution

This opt-in Go test serves the real rendered `InvoiceForm` and `EstimateForm`
and repository static assets (including production Alpine) from an ephemeral
HTTP server. No database, application server, CDN, or mocked editor is used.
Each case starts with two distinct document lines. Both create and edit forms
are covered for each document type, with valid required fields so browser
validation cannot hide an accidental submission.

Setup (Node 18+ / npm and a Chromium executable are required):

```sh
npm ci --prefix internal/templates/testdata/browser
```

Run from the repository root (run `make templ` first after editing `.templ` files):

```sh
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser go test ./internal/templates -run '^TestDocumentLineEnterBrowser$' -count=1 -v
```

`BROWSER_EXECUTABLE` defaults to `/usr/bin/chromium-browser`; set it to your
installed Chromium/Chrome executable. Ordinary Go tests skip this test.

The expected contract is that Enter in the second line's title or any numeric
input preserves existing values/order, appends a blank bottom line, and focuses
its title without submitting. Description Enter inserts a newline and retains
focus and both rows. The driver logs actual row counts/titles on failure so an
unexpected removal is distinguishable from merely failing to append.

The suite also checks held-Enter repetition, intentional Remove/Add Line,
Create Item dialog opening/closing, and an explicit Save POST with the correct
create/edit action and line-item payload. Blank appended lines must be omitted
from that payload. POSTs are accepted by the fixture server without persistence.

Enter and held-key repetition use real browser keyboard input. IME guards use
synthetic keydown events with `isComposing` and legacy key code 229; this checks
the event-handling contract, not a native operating-system IME session.

# Workflow #27 completion after integration 521de47

This follow-up resolves the three #27 acceptance blockers recorded in
`docs/v1-integration-20-46.md`. That integration was merged before work began.
No tracker issue was closed and no branch was pushed.

## Repairs and red/green evidence

1. **Current-grant intent discovery and workflow actions.** Production bearer
   HTTP initially returned all choices to a legacy `admin` with an empty Role.
   Discovery now requires current parent View, with All Records or current
   Assigned Job scope, under the shared authority fence. Status actions require
   their independent grant on that same parent. Neither the API nor web Job
   Status command has a legacy-role or legacy completed-state gate. Invisible
   Jobs return 404; a visible Job with no Status grant returns 403. Success returns
   only committed Job/status identity, avoiding an unrelated legacy-role-based
   representation reload after commit.
2. **Coherent intent snapshots.** A production HTTP test scheduled a real
   concurrent category move after the old Ent presentation read. Before the fix,
   it returned `job:pending` paired with the new `job:in_progress` meaning token.
   `DiscoverIntents` now returns presentation and both revisions from one SQL
   statement inside the parent/authority boundary. A second database race proves
   it waits for a definition editor, acquires the authority fence before parent
   rows, and returns the committed label/color/category/order/token together.
   Waiting HTTP requests also reject committed grant or assignment loss.
3. **One-shot reviewed upgrade.** Full-runner tests initially failed at migration
   044 from both pre-032 and pre-013 fixtures because intervening seed definitions
   were absent from the original review. The reviewed-cutover marker now spans
   these legacy migrations. Their label-only seeds defer to 044's canonical
   category seeding; no implicit mapping is added to the human review. The tests
   prove stale-review rejection before migration, a single successful upgrade,
   preserved Job/status/Customer identities, exact original evidence, six Invoice
   slots and an idempotent restart without another review.

The existing invoice restrictions, default/replacement/history rules, immutable
audit rollback, category/record ABA handling and integration Delivery fences are
retained. Existing tests that treated legacy role/closed-state rules as Status
authority were updated to the canonical permission and invisible-resource rules.

## Verification

Fresh disposable **UTF8** database `freefsm_v1_ticket_27_completion` on the existing
PostgreSQL 18 cluster at port 55419. All Go commands used
`GOMAXPROCS=2 GOFLAGS=-p=2`.

```sh
createdb -h 127.0.0.1 -p 55419 -U adam --template=template0 --encoding=UTF8 \
  freefsm_v1_ticket_27_completion
make lint
FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_27_completion?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55419/postgres?sslmode=disable' \
make test

FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55419/freefsm_v1_ticket_27_completion?sslmode=disable' \
NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
go test -race ./internal/handlers -run '^TestWorkflow' -count=1 -v
```

Lint and the full race-enabled suite passed, including real backup/restore.
Successful full output:
`/home/adam/.local/share/opencode/tool-output/tool_130a22c21001rOjjwBDbLMvVJ8`.
Backup ran in 375.728s, handlers in 229.171s and statusflow in 14.585s; unchanged
packages used the Go test cache. All six selected workflow HTTP/API/browser tests
passed separately in 7.156s, including persisted Chromium create/rename.

The first full-suite attempt exceeded the 600s harness timeout; it is not a pass.
Isolated race runs then passed for backup (377.441s), handlers (234.394s), database
(33.146s), and statusflow (15.860s). The complete `make test` rerun used a 900s
harness limit and passed. No timeout-driven implementation change was necessary.

Full-suite opt-in skips remain PostgreSQL <16, other domains' browser suites,
nginx scale, intercepted backup-template browser and standalone subprocess helper
entrypoints. The workflow browser selection ran explicitly with no skips. This
does not claim an exhaustive every-release or native OS/reboot matrix.

**Acceptance assessment:** the three coordinator-identified #27 blockers are
resolved, with no known remaining blocker in the workflow ticket's scoped
contract. Unrelated Job detail/list migration, wider Delivery lifecycle, complete
offline receipts/synchronization and other tickets' outstanding issues are not
certified by this assessment. The historical integration report remains a record
of its earlier checkpoint; this document supersedes its #27 blocker list only.

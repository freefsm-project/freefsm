# Slice B independent review

## Follow-up: atomic authorized save

The original review below covered the initial storage/parser slice only. Subsequent HTTP integration exposed a commit-time authorization gap between a separate domain read and intrinsic `SaveListState`; that historical review was not an endorsement of the later HTTP composition.

The current browser adapter now uses `SaveAuthorizedListState`, whose supplied domain validator executes with the fresh principal and supplied transaction under the same authority fence as CAS. Independent review and execution of the original failing HTTP race now pass. See [collection-security-review.md](collection-security-review.md) for current findings, the retained failed Billing HTTP fixture, exact evidence and incomplete integration qualification. `SaveListState` remains an intrinsic storage method; browser state writes must use the authorized operation.

## Original storage/parser review

**Verdict: no actionable blocking bugs found in the reviewed slice.**

Scope: `internal/liststate/query.go`, its tests, `internal/authorization/list_state*.go`, and `internal/database/migrations/126_list_state.up.sql`. Reviewed against #106/#18, the current implementation contract, and existing `WithinActor` authority/transaction semantics. Other slices remain in progress; this is not final ticket acceptance.

## Reviewed boundaries

- `query.go:33–63`: URL values replace route defaults as a whole; saved values are not merged here. Explicit empty values remain explicit. Unknown keys, duplicate singleton values, invalid typed values and mixed Reset/query input fail without partial output. `Bounded` checks UTF-8 and encoded size before returning accepted state. Canonicalization sorts repeated set values and does not mutate caller slices; persistence deep-copies and removes transient/transport values.
- `list_state.go:28–38,44–67`: list keys and channel are constrained; API/unknown channels reject. Reads freshly resolve the actor through the existing authority fence and select by current company/user/list. A missing row is revision zero; storage failures are not represented as empty successful preferences.
- `list_state.go:80–121`: writes use the same authority fence. Initial insert is conditional on absence; update requires the exact revision. Reset preserves an incremented empty tombstone, including when the row was absent. Stale saves cannot overwrite it. Errors return no usable success state; revision overflow is guarded.
- Migration 126 is additive: bounded text, positive revision, explicit list-key constraint, company/user foreign keys and composite primary key. No backfill, grants or business/history updates. No audit emission for browsing/presentation was added.
- Existing trusted service calls permit a sessionless Actor, consistent with `WithinActor`; this is not an anonymous HTTP entry point. C must supply the real authenticated browser actor/session. Stored values are presentation input, not query authority.

## Independent targeted execution

Used the supplied disposable PostgreSQL environment:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh`

Command after sourcing that environment:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS=-p=1 \
go test -race ./internal/liststate ./internal/authorization \
  -run 'TestParseStrictState|TestURLPrecedenceAndCanonicalRoundTrip|TestPersistentIsolationAndUnicode|TestListState' \
  -count=1 -v -timeout=10m
```

**Exit 0: 7 top-level tests and 19 subtests passed; zero skips/failures or race reports.** Tests exercised real PostgreSQL user/company isolation, session/API/disabled-user denial, storage fault rollback, competing first writes, an admitted stale save released after Reset, absent-state tombstones and populated migration preservation. Migration checks compare all preexisting application-table rows and sequences, including grants/history, and repeat migration execution.

Durable evidence:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/state-independent-review-4kUnN6nx/`

Contains `command.txt`, `test.log`, `exit.txt`, and before/after SHA-256 manifests of all reviewed source/migration files. Manifests compare identically. Other slices were active, so this is scoped provenance, not a frozen whole-repository result.

## C integration remains unqualified

This slice deliberately does not perform domain authorization of persisted filters or expose HTTP endpoints. C still owns strict raw-query decoding (do not discard `url.ParseQuery` errors), whole-URL precedence over saved state, route-specific validators, reauthorization of restored queries, protected POST persistence/Reset with the originally loaded CAS revision, and canonical/history/error rendering. Those are required integration checks, not defects attributed to unfinished C code.

No production edits, nested agents, full-suite execution, Git mutations or GitHub mutations were performed. The supplied shared ticket runtime was left running for its owner.

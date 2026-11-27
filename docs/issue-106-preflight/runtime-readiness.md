# #106 isolated runtime readiness

Provisioned 2026-11-27 03:09–03:11 UTC from base `623a8655fe44502db4078da79653e5bb3de67c1d`. Readiness smoke only; no repository acceptance/build/generation was run.

## Handoff

```sh
source /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh
```

- Durable evidence root (`E`): `/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520`, mode 0700; `env.sh` mode 0600.
- Original durable scratch: `/home/adam/.cache/freefsm-issue106-scratch-20261127T030854Z-719520`, mode 0700.
- **Active `TMPDIR`: `/home/adam/.cache/i106-719520`, mode 0700.** Shorter durable sibling is necessary for Chromium's Unix socket length limit. Keep the supplied value.
- PostgreSQL 18.6: owned postmaster PID **557530**, listening only on **127.0.0.1:45883**; private socket at `$E/socket`, data at `$E/pgdata`. Remains running for implementation tests.
- `issue106_app` owns `freefsm_issue106`; superuser/create-role/create-db/replication privileges all false. Separate `issue106_admin` URL targets this cluster's `postgres` database. Environment exports app and both required test-admin URLs. Authentication is disposable-cluster-only trust, with no reused secrets.
- Fixture files are at `$E/extensions/extension/`; cluster-only `extension_control_path` is `$E/extensions:$system`. Host extension directories were not changed.
- Private Node installation and lockfile: `$E/tooling`; driver copied read-only from existing installed tooling into `$E/tooling/geckodriver`. Existing Chromium binary is read from its shared installation; no other checkout/runtime changed.
- Browser/other suite opt-ins are **not** implicitly enabled by `env.sh`. Set the desired `BROWSER_TESTS`, `ACCESSIBILITY_TESTS`, offline-key and scale flags explicitly when running qualification.

## Actual results

| Readiness check | Result |
| --- | --- |
| PostgreSQL, pg_dump, pg_restore versions | 18.6 |
| App-role SQL connection and fixture CREATE/DROP | exit 0 |
| Chromium DOM, screenshot, axe scan | exit 0; Chromium 151.0.7922.34; zero smoke-page axe violations |
| Firefox WebDriver launch/title/quit | exit 0; Firefox 161.0; geckodriver 0.36.0 |
| Node dependencies | Node 22.22.2; npm 12.0.2; playwright-core 1.58.2; selenium-webdriver 4.51.0; axe-core 4.14.1 |
| Standalone race-enabled Go program | exit 0; Go 1.26.11-X:nodwarf5; GCC 16.2.1 |
| Rootless Podman information and disposable nginx configuration check | exit 0; Podman 5.8.9; nginx 1.31.3 |
| Secret Service default collection `Locked` property | exit 0; false (read-only check, not protected-key acceptance) |
| Shell/Node script syntax and git diff whitespace checks | exit 0 |

Exact commands, expanded setup traces, timestamps, numerical exits, inventory and source checksums are under `$E/commands`, `$E/logs` and `$E/results`. Browser artifacts are under `$E/browser-artifacts`. nginx image digest: `sha256:1d40e3eb3bf4f138de1d67193f2aa5309fcaf343eb5ffadbf5e9439de1eb1ebb`.

Failures retained:

1. Initial provision exited 127 because optional `pg_config` is absent; removed that optional query. Server/client binaries establish matching versions.
2. Second provision exited 1 because fixture files needed PostgreSQL 18's automatically appended `extension/` subdirectory. Resume provision corrected the private layout and exited 0.
3. First browser smoke exited 1 because the long scratch name exceeded Chromium's Unix socket path limit. Short durable `TMPDIR` fixed this; retry exited 0.
4. App-role `SHOW extension_control_path` exited 1, correctly denied for nonprivileged users. Admin-role configuration query and separate app-role smoke both exited 0.

No current blocker to PostgreSQL 18/browser/race implementation testing. PostgreSQL 15 negative-test runtime has not been provisioned; protected-key read/write behavior and full 1 GiB scale remain acceptance work. No manual app server or reserved app port exists; tests allocate their own listeners.

## Ownership and shutdown

Only the PostgreSQL cluster remains intentionally live. Browser smoke explicitly closed Chromium and quit Firefox. The isolated no-network container `issue106-nginx-readiness-719520` ran with `--rm`; subsequent `podman ps -a --filter name=issue106` was empty. No container or host application service remains from readiness.

On coordinator request, after test processes finish:

```sh
bash /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/commands/shutdown.sh
```

The script executes the exact owned-cluster stop:

```sh
pg_ctl -D /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/pgdata -m fast -w stop
```

It records shutdown output/exits and verifies removal of `postmaster.pid`, `pg_ctl status` exit 3, and absence of the port 45883 listener. Shutdown has **not** been executed; leave the runtime up until requested. Retain all evidence and scratch directories.

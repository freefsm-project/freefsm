#!/bin/sh
# Supporting container compatibility evidence, NOT native service/reboot proof.
# Run from the repository root after make build. No host ports or services used.
set -eu
major=${1:?usage: sh deploy/verify-postgres.sh 15|16|17|18}
case "$major" in 15|16|17|18) ;; *) echo 'expected PostgreSQL major 15, 16, 17 or 18' >&2; exit 1;; esac
test -x dist/freefsm
root=$(pwd)
scratch=$(mktemp -d)
name="freefsm-107-pg$major-$$"
trap 'podman rm -f "$name" >/dev/null 2>&1 || :; rm -rf "$scratch"' EXIT HUP INT TERM
CGO_ENABLED=0 go test -c -o "$scratch/installation.test" ./cmd/freefsm
CGO_ENABLED=0 go test -c -o "$scratch/backup-http.test" ./internal/handlers
chmod 755 "$scratch" "$scratch/installation.test" "$scratch/backup-http.test"
filter='Test(StartupRejects|HostRecovery|InstallationBootstrap)'
if [ "$major" = 15 ]; then filter='TestStartupRejectsUnsupportedServerBeforeBootstrap'; fi
podman run --rm --name "$name" --network none --user postgres --tmpfs /tmp:rw,mode=1777 \
    -v "$root/dist/freefsm:/opt/freefsm:ro,z" \
    -v "$scratch/installation.test:/opt/installation.test:ro,Z" \
    -v "$scratch/backup-http.test:/opt/backup-http.test:ro,Z" \
    -e FREEFSM_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55507/freefsm_test?sslmode=disable' \
    -e FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://postgres@127.0.0.1:55507/postgres?sslmode=disable' \
    -e FREEFSM_INSTALLATION_BINARY=/opt/freefsm \
    -e PG_TEST_MAJOR="$major" \
    -e TEST_FILTER="$filter" "docker.io/library/postgres:$major" sh -c '
        initdb -D /tmp/pg --auth=trust --no-locale --encoding=UTF8 &&
        pg_ctl -D /tmp/pg -l /tmp/postgres.log -o "-p 55507 -h 127.0.0.1" -w start &&
        createdb -h 127.0.0.1 -p 55507 freefsm_test &&
        postgres --version && pg_dump --version && pg_restore --version &&
        sha256sum /opt/freefsm && /opt/installation.test -test.v -test.run "$TEST_FILTER" &&
        if [ "$PG_TEST_MAJOR" != 15 ]; then
            /opt/backup-http.test -test.v -test.run "^TestBackup(HTTPRoundTrip|HistoricalDomainRoundTrip)$"
        fi
    '
podman image inspect "docker.io/library/postgres:$major" --format '{{.Digest}}'

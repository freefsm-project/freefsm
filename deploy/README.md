Deploy
==========

## Prerequisites

- PostgreSQL 16+
- FreeFSM binary (build with `make build`)

## Build

The Makefile is written for **GNU Make**. On FreeBSD, install `gmake` first:

```sh
# FreeBSD
sudo pkg install gmake

# Build on both Linux and FreeBSD
gmake build
# or on Linux
make build
```

After building, verify the binary checksum to confirm identical output:

```sh
gmake checksum
```

## Setup

After first start, visit `/setup` on your server and enter the `SETUP_TOKEN`
from your config to create the first admin account. After that, remove the
token from the config and restart.

## Linux (systemd)

1.  Create the `freefsm` user:
    `sudo useradd --system --user-group --create-home --home-dir /var/lib/freefsm freefsm`

2.  Copy the binary:
    `sudo cp dist/freefsm /usr/local/bin/freefsm`
    `sudo chmod +x /usr/local/bin/freefsm`

3.  Install the service and config:
    `sudo make install-linux`

4.  Edit `/etc/freefsm.conf` with your database credentials and session secret.

5.  Start the service:
    `sudo systemctl enable --now freefsm`

6.  (Optional) Set up the Nginx reverse proxy:
    `sudo cp deploy/linux/freefsm.nginx.conf /etc/nginx/sites-available/freefsm`
    Edit `/etc/nginx/sites-available/freefsm` — replace `example.com` with your domain.
    `sudo ln -s /etc/nginx/sites-available/freefsm /etc/nginx/sites-enabled/`
    `sudo systemctl reload nginx`

## FreeBSD (rc.d)

1.  Install prerequisites:
    `sudo pkg install gmake`

2.  Create the `freefsm` user:
    `sudo pw useradd freefsm -s /usr/sbin/nologin -d /var/db/freefsm -m`

3.  Copy the binary:
    `sudo cp dist/freefsm /usr/local/bin/freefsm`
    `sudo chmod +x /usr/local/bin/freefsm`

4.  Install the service and config:
    `sudo gmake install-freebsd`

5.  Add to `/etc/rc.conf`:
    `freefsm_enable="YES"`
    `freefsm_config="/usr/local/etc/freefsm.conf"`

6.  Edit `/usr/local/etc/freefsm.conf` with your database credentials and session secret.

7.  Start the service:
    `sudo service freefsm start`

## Cross-Platform Check

To verify both platforms produce identical binaries:

```sh
# On Linux
make build && make checksum
# On FreeBSD
gmake build && gmake checksum
# Compare the SHA256 hashes
```

If the checksums differ, check that:
- Both platforms use the same `go` version (`go version`)
- Both use GNU Make (not BSD `make` on FreeBSD)
- Both checked out the same git commit

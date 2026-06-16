Deploy
==========

## Prerequisites

- PostgreSQL 16+
- FreeFSM binary (build with `make build`)

## Linux (systemd)

1.  Create the `freefsm` user:
    `sudo useradd --system --user-group --create-home --home-dir /var/lib/freefsm freefsm`

2.  Copy the binary:
    `sudo cp freefsm /usr/local/bin/freefsm`
    `sudo chmod +x /usr/local/bin/freefsm`

3.  Copy the systemd service file:
    `sudo cp deploy/linux/freefsm.service /etc/systemd/system/freefsm.service`

4.  Create and edit the environment file:
    `sudo cp deploy/linux/freefsm.conf.sample /etc/freefsm.conf`
    `sudo chown root:root /etc/freefsm.conf`
    `sudo chmod 600 /etc/freefsm.conf`
    Edit `/etc/freefsm.conf` with your database credentials and session secret.

5.  Start the service:
    `sudo systemctl daemon-reload`
    `sudo systemctl enable --now freefsm`

6.  (Optional) Set up the Nginx reverse proxy:
    `sudo cp deploy/linux/freefsm.nginx.conf /etc/nginx/sites-available/freefsm`
    Edit `/etc/nginx/sites-available/freefsm` — replace `example.com` with your domain.
    `sudo ln -s /etc/nginx/sites-available/freefsm /etc/nginx/sites-enabled/`
    `sudo systemctl reload nginx`

## FreeBSD (rc.d)

1.  Create the `freefsm` user:
    `sudo pw useradd freefsm -s /usr/sbin/nologin -d /var/db/freefsm -m`

3.  Copy the rc.d script:
    `sudo cp deploy/freebsd/freefsm /etc/rc.d/freefsm`
    `sudo chmod +x /etc/rc.d/freefsm`

4.  Add to `/etc/rc.conf`:
    `freefsm_enable="YES"`
    `freefsm_config="/usr/local/etc/freefsm.conf"`

5.  Start the service:
    `sudo service freefsm start`

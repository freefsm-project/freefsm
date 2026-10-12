#!/bin/sh
# Run from any working directory. DESTDIR supports packaging/disposable checks;
# / installs on this host. The binary is installed by the Makefile.
set -eu
root=${1:?supply destination root (use / for the live host)}
service_user=${2:-freefsm}
service_group=${3:-freefsm}
source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
config="$root/usr/local/etc/freefsm.conf"

if [ -e "$root/etc/rc.d/freefsm" ]; then
    echo 'Legacy /etc/rc.d/freefsm exists: stop the service, review and relocate that script to /usr/local/etc/rc.d before reinstalling' >&2
    exit 1
fi
install -d "$root/usr/local/etc/rc.d"
install -m 755 "$source_dir/freefsm" "$root/usr/local/etc/rc.d/freefsm"
install -m 644 "$source_dir/freefsm.conf.sample" "$root/usr/local/etc/freefsm.conf.sample"
if [ -L "$config" ] || { [ -e "$config" ] && [ ! -f "$config" ]; }; then
    echo 'Live FreeFSM configuration must be a regular file, not a symlink' >&2
    exit 1
fi
if [ ! -e "$config" ]; then
    install -o "$service_user" -g "$service_group" -m 600 "$source_dir/freefsm.conf.sample" "$config"
else
    # Retain operator values, repairing permissions of older installations.
    chown "$service_user:$service_group" "$config"
    chmod 600 "$config"
fi
install -d -o "$service_user" -g "$service_group" -m 750 "$root/var/db/freefsm"
install -d -o "$service_user" -g "$service_group" -m 750 "$root/var/log/freefsm" "$root/var/db/freefsm/uploads"
install -d -o "$service_user" -g "$service_group" -m 700 "$root/var/db/freefsm/state"

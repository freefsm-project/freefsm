#!/bin/sh
# Staged filesystem installation; service activation is the caller's decision.
set -eu
root=${1:?supply destination root (use / for the live host)}
service_user=${2:-freefsm}
service_group=${3:-freefsm}
source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
config="$root/etc/freefsm.conf"
install -d "$root/etc/systemd/system"
install -m 644 "$source_dir/freefsm.service" "$root/etc/systemd/system/freefsm.service"
if [ -L "$config" ] || { [ -e "$config" ] && [ ! -f "$config" ]; }; then
    echo 'Live FreeFSM configuration must be a regular file, not a symlink' >&2
    exit 1
fi
if [ ! -e "$config" ]; then
    install -m 600 "$source_dir/freefsm.conf.sample" "$config"
else
    chmod 600 "$config"
fi
install -d -o "$service_user" -g "$service_group" -m 750 "$root/var/lib/freefsm" "$root/var/log/freefsm" "$root/var/lib/freefsm/uploads"
install -d -o "$service_user" -g "$service_group" -m 700 "$root/var/lib/freefsm/state"

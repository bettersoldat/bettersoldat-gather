#!/bin/sh
# Starts the server on the settings in the environment. On fly.io a UDP service must
# listen on the fly-global-services address, not on every one, or its answers leave
# from another address and never reach the player: when that name resolves, the server
# binds it (sv_ip). SV_IP set by hand wins; anywhere else it listens on every address.
# A gather server is private: it never lists itself with the lobby (sv_public 0).
set -eu
cd /app

bind="${SV_IP:-}"
if [ -z "$bind" ] && resolved=$(getent hosts fly-global-services 2>/dev/null); then
    bind=$(printf '%s\n' "$resolved" | awk 'NR == 1 { print $1 }')
    echo "entrypoint: fly-global-services is $bind; listening there"
fi

exec ./soldatreloaded-server \
    +sv_ip "$bind" \
    +sv_port "${SV_PORT:-23073}" \
    +sv_hostname "${SV_HOSTNAME:-SoldatReloaded gather}" \
    +map "${SV_MAP:-ctf_Ash}" \
    +sv_gamemode 2 \
    +sv_timelimit "${SV_TIMELIMIT:-10}" \
    +sv_killlimit "${SV_KILLLIMIT:-10}" \
    +sv_script scripts/server.lua \
    +sv_public 0 \
    "$@"

#!/usr/bin/env bash
set -euo pipefail

API_BIN="./api"
HOSTNAME_IN="sandbox"
CG_ROOT="/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service"
CG="$CG_ROOT/sandbox"

if [ -d "$CG" ]; then
    rmdir "$CG" 2>/dev/null || true
fi

mkdir -p "$CG"

echo "+cpu +memory +pids" > "$CG_ROOT/cgroup.subtree_control" 2>/dev/null || true

echo 0 > "$CG/memory.swap.max" 2>/dev/null || true
echo 100M > "$CG/memory.max"
echo "50000 100000" > "$CG/cpu.max"
echo 15 > "$CG/pids.max"

echo $$ > "$CG/cgroup.procs"

unshare \
    --pid --fork \
    --mount --mount-proc \
    --net \
    --uts \
    --ipc \
    --user --map-root-user \
    -- /bin/bash -c '
        set -euo pipefail
        hostname "$0"
        ip link set lo up 2>/dev/null || true
        exec setpriv \
        --inh-caps=-all \
        --ambient-caps=-all \
        --bounding-set=-all \
        --no-new-privs \
        -- ./sandbox "$1"
    ' "$HOSTNAME_IN" "$API_BIN"


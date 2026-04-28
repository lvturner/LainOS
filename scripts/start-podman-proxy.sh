#!/bin/bash
set -e

PODMAN_SOCK="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock"
PROXY_PORT=28888

pkill -f "socat.*TCP-LISTEN:${PROXY_PORT}" 2>/dev/null || true
sleep 0.5

if [ ! -S "$PODMAN_SOCK" ]; then
    echo "ERROR: podman socket not found at $PODMAN_SOCK"
    exit 1
fi

setsid socat TCP-LISTEN:${PROXY_PORT},reuseaddr,fork,bind=127.0.0.1 UNIX-CONNECT:${PODMAN_SOCK} >/dev/null 2>&1 &
disown
sleep 0.5
if ss -tlnp | grep -q ":${PROXY_PORT}"; then
    echo "socat proxy started on TCP localhost:${PROXY_PORT} -> ${PODMAN_SOCK}"
else
    echo "ERROR: socat proxy failed to start"
    exit 1
fi

#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source "$SCRIPT_DIR/scripts/common.sh"

if ! _detect_runtime; then
    echo "No container runtime found. Run ./start.sh first."
    exit 1
fi

STATE=$($RUNTIME exec lainos systemctl is-system-running 2>/dev/null || true)
if [[ "$STATE" != "running" && "$STATE" != "degraded" ]]; then
    echo "Container is not running. Start it first with ./start.sh"
    exit 1
fi

exec $RUNTIME exec -it -u lainos -w /home/lainos lainos /usr/local/bin/lain

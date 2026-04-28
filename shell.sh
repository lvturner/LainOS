#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if ! podman exec lainos systemctl is-system-running >/dev/null 2>&1; then
    echo "Container is not running. Start it first with ./start.sh"
    exit 1
fi

exec podman exec -it -u lainos -w /home/lainos lainos /usr/local/bin/lain

#!/usr/bin/env bash
set -euo pipefail

SNAPSHOT_INTERVAL="${SNAPSHOT_INTERVAL:-3600}"
SNAPSHOT_SOURCE="${SNAPSHOT_SOURCE:-/home/lainos}"
SNAPSHOT_DIR="${SNAPSHOT_DIR:-/snapshots}"
SNAPSHOT_RETENTION_DAYS="${SNAPSHOT_RETENTION_DAYS:-30}"
SNAPSHOT_EXCLUDE="${SNAPSHOT_EXCLUDE:-\.cache|\.camofox|\.camoufox|\.npm|\.local|\.fontconfig|/tmp}"

if [ -f /home/lainos/config/snapshot.conf ]; then
    source /home/lainos/config/snapshot.conf
fi

if ! btrfs subvolume show "$SNAPSHOT_SOURCE" &>/dev/null; then
    echo "ERROR: $SNAPSHOT_SOURCE is not a btrfs subvolume" >&2
    echo "  Run scripts/migrate-to-subvolume.sh on the host first" >&2
    exit 1
fi

if [ ! -d "$SNAPSHOT_DIR" ]; then
    echo "ERROR: Snapshot directory $SNAPSHOT_DIR does not exist" >&2
    exit 1
fi

if ! command -v inotifywait &>/dev/null; then
    echo "ERROR: inotifywait not found. Install inotify-tools." >&2
    exit 1
fi

echo "Watching $SNAPSHOT_SOURCE for changes (interval: ${SNAPSHOT_INTERVAL}s)"

last_snapshot=0

inotifywait -m -r -e modify,create,delete,moved_to,moved_from \
    --exclude "$SNAPSHOT_EXCLUDE" \
    "$SNAPSHOT_SOURCE" 2>/dev/null | while read -r _; do
    now=$(date +%s)
    elapsed=$((now - last_snapshot))
    if [ "$elapsed" -ge "$SNAPSHOT_INTERVAL" ]; then
        if /usr/local/bin/home-snapshot.sh; then
            last_snapshot=$now
        fi
    fi
done

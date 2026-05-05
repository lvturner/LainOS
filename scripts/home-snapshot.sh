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
    echo "  Create it and ensure it's on the same btrfs filesystem" >&2
    exit 1
fi

CURRENT_GEN=$(btrfs subvolume show "$SNAPSHOT_SOURCE" | grep 'Generation:' | awk '{print $2}')

LAST_SNAPSHOT=$(ls -1t "$SNAPSHOT_DIR"/home-* 2>/dev/null | head -1)
if [ -n "$LAST_SNAPSHOT" ]; then
    LAST_GEN=$(btrfs subvolume show "$LAST_SNAPSHOT" 2>/dev/null | grep 'Generation:' | awk '{print $2}' || echo "0")
    if [ "$CURRENT_GEN" = "$LAST_GEN" ]; then
        exit 0
    fi
fi

TIMESTAMP=$(date +%Y-%m-%dT%H-%M-%S)
SNAPSHOT_PATH="$SNAPSHOT_DIR/home-$TIMESTAMP"

btrfs subvolume snapshot -r "$SNAPSHOT_SOURCE" "$SNAPSHOT_PATH"

echo "Snapshot created: $SNAPSHOT_PATH (generation: $CURRENT_GEN)"

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DATA_HOME="$PROJECT_DIR/.data/home"
DATA_SNAPSHOTS="$PROJECT_DIR/.data/snapshots"

if stat -f -c '%T' "$PROJECT_DIR/.data" 2>/dev/null | grep -q btrfs; then
    :
else
    echo "ERROR: $PROJECT_DIR/.data is not on a btrfs filesystem"
    echo "  Current filesystem: $(stat -f -c '%T' "$PROJECT_DIR/.data")"
    exit 1
fi

if btrfs subvolume show "$DATA_HOME" &>/dev/null; then
    echo "OK: $DATA_HOME is already a btrfs subvolume"
    mkdir -p "$DATA_SNAPSHOTS"
    echo "OK: Snapshots directory ready at $DATA_SNAPSHOTS"
    exit 0
fi

if podman ps --format '{{.Names}}' 2>/dev/null | grep -q '^lainos$'; then
    echo "ERROR: Container 'lainos' is running. Stop it first:"
    echo "  podman-compose down"
    exit 1
fi

echo "Migrating $DATA_HOME to a btrfs subvolume..."

DATA_MIGRATE="$PROJECT_DIR/.data/home_migrate"
mv "$DATA_HOME" "$DATA_MIGRATE"

btrfs subvolume create "$DATA_HOME"

cp -a --reflink=auto "$DATA_MIGRATE/." "$DATA_HOME/"

rm -rf "$DATA_MIGRATE"

mkdir -p "$DATA_SNAPSHOTS"

echo ""
echo "Migration complete!"
echo ""
echo "Next steps:"
echo "  1. podman-compose up -d --build"
echo "  2. Verify inside the container: btrfs subvolume show /home/lainos"
echo "  3. Check snapshot service: systemctl --user status home-snapshot"

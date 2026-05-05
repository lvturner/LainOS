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

if [ ! -d "$SNAPSHOT_DIR" ]; then
    exit 0
fi

now=$(date +%s)
max_age=$((SNAPSHOT_RETENTION_DAYS * 86400))
kept=0
deleted=0

for snap in $(ls -1d "$SNAPSHOT_DIR"/home-* 2>/dev/null | sort -r); do
    name=$(basename "$snap")
    ts_str="${name#home-}"
    ts_str="${ts_str//-/:}"
    ts_str="${ts_str:0:10} ${ts_str:11:8}"

    snap_epoch=$(date -d "$ts_str" +%s 2>/dev/null || continue)
    age=$((now - snap_epoch))

    if [ "$age" -ge "$max_age" ]; then
        echo "DELETE (older than ${SNAPSHOT_RETENTION_DAYS} days): $name"
        btrfs subvolume delete "$snap" 2>/dev/null || true
        deleted=$((deleted + 1))
        continue
    fi

    snap_min=$(date -d "@$snap_epoch" +%M 2>/dev/null)
    snap_sec=$(date -d "@$snap_epoch" +%S 2>/dev/null)
    snap_hour=$(date -d "@$snap_epoch" +%H 2>/dev/null)
    snap_dow=$(date -d "@$snap_epoch" +%u 2>/dev/null)

    if [ "$age" -lt 3600 ]; then
        kept=$((kept + 1))
        continue
    fi

    if [ "$age" -lt 86400 ]; then
        if [ "$snap_min" = "00" ] && [ "$snap_sec" = "00" ]; then
            kept=$((kept + 1))
            continue
        fi
        echo "DELETE (< 24h, not top of hour): $name"
        btrfs subvolume delete "$snap" 2>/dev/null || true
        deleted=$((deleted + 1))
        continue
    fi

    if [ "$age" -lt 604800 ]; then
        if [ "$snap_hour" = "00" ] && [ "$snap_min" = "00" ]; then
            kept=$((kept + 1))
            continue
        fi
        echo "DELETE (< 7d, not midnight): $name"
        btrfs subvolume delete "$snap" 2>/dev/null || true
        deleted=$((deleted + 1))
        continue
    fi

    if [ "$snap_dow" = "7" ] && [ "$snap_hour" = "00" ] && [ "$snap_min" = "00" ]; then
        kept=$((kept + 1))
        continue
    fi

    echo "DELETE (< 30d, not Sunday midnight): $name"
    btrfs subvolume delete "$snap" 2>/dev/null || true
    deleted=$((deleted + 1))
done

echo "Cleanup complete: kept=$kept deleted=$deleted"

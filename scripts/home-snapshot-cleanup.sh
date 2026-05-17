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

shopt -s nullglob
snapshots=("$SNAPSHOT_DIR"/home-*)
shopt -u nullglob

if [ ${#snapshots[@]} -eq 0 ]; then
    echo "No snapshots found"
    exit 0
fi

IFS=$'\n' sorted=($(printf '%s\n' "${snapshots[@]}" | sort))
unset IFS

now=$(date +%s)
max_age=$((SNAPSHOT_RETENTION_DAYS * 86400))

declare -A hour_bucket
declare -A day_bucket
declare -A week_bucket

kept=0
deleted=0

snap_epoch() {
    local name ts_str
    name=$(basename "$1")
    ts_str="${name#home-}"
    ts_str="${ts_str//-/:}"
    ts_str="${ts_str:0:10} ${ts_str:11:8}"
    date -d "$ts_str" +%s 2>/dev/null || echo ""
}

for snap in "${sorted[@]}"; do
    epoch=$(snap_epoch "$snap")
    if [ -z "$epoch" ]; then
        continue
    fi

    age=$((now - epoch))

    if [ "$age" -ge "$max_age" ]; then
        echo "DELETE (older than ${SNAPSHOT_RETENTION_DAYS} days): $(basename "$snap")"
        btrfs subvolume delete "$snap" 2>/dev/null || true
        deleted=$((deleted + 1))
        continue
    fi

    if [ "$age" -lt 3600 ]; then
        echo "KEEP (< 1h): $(basename "$snap")"
        kept=$((kept + 1))
        continue
    fi

    if [ "$age" -lt 86400 ]; then
        bucket_key=$(date -d "@$epoch" +%Y-%m-%d_%H)
        existing="${hour_bucket[$bucket_key]:-}"
        if [ -z "$existing" ]; then
            hour_bucket[$bucket_key]="$snap"
            echo "KEEP (< 24h, best for hour $bucket_key): $(basename "$snap")"
            kept=$((kept + 1))
        else
            existing_epoch=$(snap_epoch "$existing")
            existing_min=$(date -d "@$existing_epoch" +%M | sed 's/^0//')
            existing_sec=$(date -d "@$existing_epoch" +%S | sed 's/^0//')
            existing_dist=$((existing_min * 60 + existing_sec))
            if [ "$existing_dist" -gt 1800 ]; then
                existing_dist=$((3600 - existing_dist))
            fi

            current_min=$(date -d "@$epoch" +%M | sed 's/^0//')
            current_sec=$(date -d "@$epoch" +%S | sed 's/^0//')
            current_dist=$((current_min * 60 + current_sec))
            if [ "$current_dist" -gt 1800 ]; then
                current_dist=$((3600 - current_dist))
            fi

            if [ "$current_dist" -lt "$existing_dist" ]; then
                echo "DELETE (< 24h, superseded): $(basename "$existing")"
                btrfs subvolume delete "$existing" 2>/dev/null || true
                deleted=$((deleted + 1))
                hour_bucket[$bucket_key]="$snap"
                echo "KEEP (< 24h, best for hour $bucket_key): $(basename "$snap")"
                kept=$((kept + 1))
            else
                echo "DELETE (< 24h, not best for hour): $(basename "$snap")"
                btrfs subvolume delete "$snap" 2>/dev/null || true
                deleted=$((deleted + 1))
            fi
        fi
        continue
    fi

    if [ "$age" -lt 604800 ]; then
        bucket_key=$(date -d "@$epoch" +%Y-%m-%d)
        existing="${day_bucket[$bucket_key]:-}"
        if [ -z "$existing" ]; then
            day_bucket[$bucket_key]="$snap"
            echo "KEEP (< 7d, best for day $bucket_key): $(basename "$snap")"
            kept=$((kept + 1))
        else
            existing_epoch=$(snap_epoch "$existing")
            existing_hour=$(date -d "@$existing_epoch" +%H | sed 's/^0//')
            existing_min=$(date -d "@$existing_epoch" +%M | sed 's/^0//')
            existing_dist=$((existing_hour * 60 + existing_min))
            if [ "$existing_dist" -gt 720 ]; then
                existing_dist=$((1440 - existing_dist))
            fi

            current_hour=$(date -d "@$epoch" +%H | sed 's/^0//')
            current_min=$(date -d "@$epoch" +%M | sed 's/^0//')
            current_dist=$((current_hour * 60 + current_min))
            if [ "$current_dist" -gt 720 ]; then
                current_dist=$((1440 - current_dist))
            fi

            if [ "$current_dist" -lt "$existing_dist" ]; then
                echo "DELETE (< 7d, superseded): $(basename "$existing")"
                btrfs subvolume delete "$existing" 2>/dev/null || true
                deleted=$((deleted + 1))
                day_bucket[$bucket_key]="$snap"
                echo "KEEP (< 7d, best for day $bucket_key): $(basename "$snap")"
                kept=$((kept + 1))
            else
                echo "DELETE (< 7d, not best for day): $(basename "$snap")"
                btrfs subvolume delete "$snap" 2>/dev/null || true
                deleted=$((deleted + 1))
            fi
        fi
        continue
    fi

    dow=$(date -d "@$epoch" +%u)
    bucket_key=""
    if [ "$dow" = "7" ]; then
        bucket_key=$(date -d "@$epoch" +%Y-%m-%d)
    else
        prev_sunday=$(date -d "@$((epoch - dow * 86400))" +%Y-%m-%d)
        bucket_key="$prev_sunday"
    fi

    existing="${week_bucket[$bucket_key]:-}"
    if [ -z "$existing" ]; then
        week_bucket[$bucket_key]="$snap"
        echo "KEEP (< 30d, best for week $bucket_key): $(basename "$snap")"
        kept=$((kept + 1))
    else
        existing_epoch=$(snap_epoch "$existing")
        existing_hour=$(date -d "@$existing_epoch" +%H | sed 's/^0//')
        existing_min=$(date -d "@$existing_epoch" +%M | sed 's/^0//')
        existing_dist=$((existing_hour * 60 + existing_min))
        if [ "$existing_dist" -gt 720 ]; then
            existing_dist=$((1440 - existing_dist))
        fi

        current_hour=$(date -d "@$epoch" +%H | sed 's/^0//')
        current_min=$(date -d "@$epoch" +%M | sed 's/^0//')
        current_dist=$((current_hour * 60 + current_min))
        if [ "$current_dist" -gt 720 ]; then
            current_dist=$((1440 - current_dist))
        fi

        if [ "$current_dist" -lt "$existing_dist" ]; then
            echo "DELETE (< 30d, superseded): $(basename "$existing")"
            btrfs subvolume delete "$existing" 2>/dev/null || true
            deleted=$((deleted + 1))
            week_bucket[$bucket_key]="$snap"
            echo "KEEP (< 30d, best for week $bucket_key): $(basename "$snap")"
            kept=$((kept + 1))
        else
            echo "DELETE (< 30d, not best for week): $(basename "$snap")"
            btrfs subvolume delete "$snap" 2>/dev/null || true
            deleted=$((deleted + 1))
        fi
    fi
done

echo "Cleanup complete: kept=$kept deleted=$deleted"

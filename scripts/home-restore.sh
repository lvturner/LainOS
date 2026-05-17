#!/usr/bin/env bash
set -euo pipefail

SNAPSHOT_DIR="${SNAPSHOT_DIR:-/snapshots}"
SNAPSHOT_SOURCE="${SNAPSHOT_SOURCE:-/home/lainos}"

if [ -f /home/lainos/config/snapshot.conf ]; then
    source /home/lainos/config/snapshot.conf
fi

DRY_RUN=""
CONFIRM=""

SERVICES=(wh-gateway camofox cloudflared)

usage() {
    cat <<'EOF'
Usage: home-restore.sh [OPTIONS] COMMAND

Commands:
  --list                     List available snapshots
  --diff <snapshot>          Show changed files between snapshot and current state
  --restore <snapshot> <path>  Restore a single file/directory from snapshot
  --full <snapshot>          Full restore: stop services, rsync, restart services

Options:
  --dry-run                  Preview operation without making changes
  --yes                      Skip confirmation prompt
  -h, --help                 Show this help
EOF
}

log() {
    echo ":: $*"
}

log_dry() {
    echo "[DRY RUN] $*"
}

confirm() {
    if [ -n "$CONFIRM" ]; then
        return 0
    fi
    echo -n "Proceed? [y/N] "
    read -r answer
    case "$answer" in
        [yY]|[yY][eE][sS]) return 0 ;;
        *) echo "Aborted."; return 1 ;;
    esac
}

stop_services() {
    for svc in "${SERVICES[@]}"; do
        if systemctl is-active --quiet "$svc" 2>/dev/null; then
            if [ -n "$DRY_RUN" ]; then
                log_dry "Would stop $svc"
            else
                log "Stopping $svc"
                systemctl stop "$svc"
            fi
        fi
    done
    if systemctl is-active --quiet cloudflared 2>/dev/null; then
        if [ -n "$DRY_RUN" ]; then
            log_dry "Would stop cloudflared"
        else
            log "Stopping cloudflared"
            systemctl --user stop cloudflared 2>/dev/null || true
        fi
    fi
}

start_services() {
    for svc in "${SERVICES[@]}"; do
        if [ -n "$DRY_RUN" ]; then
            log_dry "Would start $svc"
        else
            log "Starting $svc"
            systemctl start "$svc" 2>/dev/null || true
        fi
    done
    if [ -n "$DRY_RUN" ]; then
        log_dry "Would start cloudflared"
    else
        log "Starting cloudflared"
        systemctl --user start cloudflared 2>/dev/null || true
    fi
}

validate_snapshot() {
    local snap="$1"
    if [ ! -d "$snap" ]; then
        echo "ERROR: Snapshot directory not found: $snap" >&2
        exit 1
    fi
    if [ ! -d "$snap/home" ] && [ ! -f "$snap/.bashrc" ] && [ ! -d "$snap/config" ]; then
        echo "WARNING: $snap does not appear to be a home directory snapshot" >&2
    fi
}

do_list() {
    shopt -s nullglob
    local snapshots=("$SNAPSHOT_DIR"/home-*)
    shopt -u nullglob

    if [ ${#snapshots[@]} -eq 0 ]; then
        echo "No snapshots found in $SNAPSHOT_DIR"
        return 0
    fi

    printf "%-40s %s\n" "SNAPSHOT" "SIZE"
    for snap in "${snapshots[@]}"; do
        local name size
        name=$(basename "$snap")
        size=$(du -sh "$snap" 2>/dev/null | cut -f1 || echo "unknown")
        printf "%-40s %s\n" "$name" "$size"
    done
}

do_diff() {
    local snap="$1"
    validate_snapshot "$snap"

    log "Comparing $snap with $SNAPSHOT_SOURCE"
    if [ -n "$DRY_RUN" ]; then
        log_dry "Would run: diff -rq $snap/ $SNAPSHOT_SOURCE/"
        return 0
    fi

    diff -rq "$snap/" "$SNAPSHOT_SOURCE/" 2>/dev/null | head -200
}

do_restore() {
    local snap="$1"
    local rel_path="$2"
    validate_snapshot "$snap"

    local source="$snap/$rel_path"
    local dest="$SNAPSHOT_SOURCE/$rel_path"

    if [ ! -e "$source" ]; then
        echo "ERROR: Path not found in snapshot: $source" >&2
        exit 1
    fi

    if [ -n "$DRY_RUN" ]; then
        log_dry "Would restore: $source -> $dest"
        log_dry "Command: cp -a --reflink=auto \"$source\" \"$dest\""
        return 0
    fi

    log "Restoring: $source -> $dest"
    confirm || exit 1

    local dest_parent
    dest_parent=$(dirname "$dest")
    mkdir -p "$dest_parent"

    cp -a --reflink=auto "$source" "$dest"
    log "Restored successfully"
}

do_full() {
    local snap="$1"
    validate_snapshot "$snap"

    if [ -n "$DRY_RUN" ]; then
        log_dry "Would perform full restore from $snap to $SNAPSHOT_SOURCE"
        log_dry "Would stop services: ${SERVICES[*]} cloudflared"
        log_dry "Would run: rsync -a --delete $snap/ $SNAPSHOT_SOURCE/"
        log_dry "Would restart services: ${SERVICES[*]} cloudflared"
        return 0
    fi

    log "Full restore from $snap to $SNAPSHOT_SOURCE"
    log "This will:"
    log "  1. Stop running services (gateway, camofox, cloudflared)"
    log "  2. rsync --delete from snapshot to $SNAPSHOT_SOURCE"
    log "  3. Restart all services"
    echo ""
    confirm || exit 1

    stop_services

    log "Syncing files..."
    rsync -a --delete "$snap/" "$SNAPSHOT_SOURCE/"
    log "Sync complete"

    start_services

    log "Full restore complete"
}

COMMAND=""
SNAPSHOT=""
RESTORE_PATH=""

while [ $# -gt 0 ]; do
    case "$1" in
        --list)
            COMMAND="list"
            shift
            ;;
        --diff)
            COMMAND="diff"
            SNAPSHOT="$2"
            shift 2
            ;;
        --restore)
            COMMAND="restore"
            SNAPSHOT="$2"
            RESTORE_PATH="$3"
            shift 3
            ;;
        --full)
            COMMAND="full"
            SNAPSHOT="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        --yes|-y)
            CONFIRM=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
done

case "$COMMAND" in
    list)
        do_list
        ;;
    diff)
        if [ -z "$SNAPSHOT" ]; then
            echo "ERROR: --diff requires a snapshot path" >&2
            exit 1
        fi
        do_diff "$SNAPSHOT"
        ;;
    restore)
        if [ -z "$SNAPSHOT" ] || [ -z "$RESTORE_PATH" ]; then
            echo "ERROR: --restore requires <snapshot> and <path>" >&2
            exit 1
        fi
        do_restore "$SNAPSHOT" "$RESTORE_PATH"
        ;;
    full)
        if [ -z "$SNAPSHOT" ]; then
            echo "ERROR: --full requires a snapshot path" >&2
            exit 1
        fi
        do_full "$SNAPSHOT"
        ;;
    *)
        usage >&2
        exit 1
        ;;
esac

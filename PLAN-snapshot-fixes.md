# Plan: Snapshot System Fixes

## Problem

The snapshot system has several critical issues that make it unreliable for actual data recovery.

## Issues

### 1. Retention logic deletes nearly all snapshots (CRITICAL)

**File:** `scripts/home-snapshot-cleanup.sh`

The 24-hour retention tier keeps only snapshots where `snap_min = "00" && snap_sec = "00"` (exactly top of hour). Since `inotifywait` triggers debounced snapshots at arbitrary times, almost no snapshots will land on `HH:00:00`. The 7-day tier has the same problem with midnight (`snap_hour = "00"`). Result: after 1 hour, most snapshots are pruned aggressively.

**Fix:** Replace exact timestamp matching with time-bucket grouping. Sort snapshots, then for each tier, keep the closest snapshot to each bucket boundary:

```
< 1h:   keep all
< 24h:  keep 1 per hour (find closest to each HH:00:00)
< 7d:   keep 1 per day (find closest to each 00:00:00)
< 30d:  keep 1 per week (find closest to each Sunday 00:00:00)
> 30d:  delete
```

Algorithm: iterate snapshots oldest-first, group by bucket (hour/date/week), keep the one closest to the bucket boundary in each group.

### 2. No restore command

**Files:** `scripts/` (new file), `docs/snapshots.md`

There is no `home-restore.sh` script. Users must manually run `cp --reflink` or `rsync` commands. The documented "nuclear option" (`rsync -a --delete` into a live subvolume) risks corrupting running services.

**Fix:** Create `scripts/home-restore.sh` with:
- `--list` — list available snapshots with timestamps and sizes
- `--diff <snapshot>` — show changed files between snapshot and current state
- `--restore <snapshot> <path>` — restore a single file/directory with `--reflink=auto`
- `--full <snapshot>` — stop services, rsync from snapshot, restart services
- Confirmation prompt before any destructive operation
- Service stop/start for full restore (wh-gateway, camofox, cloudflared)
- `--dry-run` flag for all operations

### 3. Service scope mismatch in docs

**File:** `docs/snapshots.md`, `systemd/home-snapshot.service`

The docs say `systemctl --user restart home-snapshot` but the service is installed at `/usr/lib/systemd/system/home-snapshot.service` (system scope, no `User=` directive). It runs as root. `systemctl --user` won't find it.

**Fix (option A):** Move to user scope. Change the unit to a user service with `User=lainos`, install to `/etc/systemd/user/`, update preset. But snapshot commands need root for `btrfs subvolume snapshot`.

**Fix (option B, recommended):** Keep as system service. Fix all docs to use `systemctl restart home-snapshot` (no `--user`). Update `docs/snapshots.md`, `config/agents.example.md`, and `AGENTS.md`.

### 4. Snapshot watcher silently swallows failures

**File:** `scripts/home-snapshot-watch.sh`

When `home-snapshot.sh` fails (disk full, permissions, etc.), `last_snapshot` is still set to `$now`, preventing retries for `SNAPSHOT_INTERVAL` seconds.

**Fix:** Only update `last_snapshot` on success:

```bash
if /usr/local/bin/home-snapshot.sh; then
    last_snapshot=$now
fi
```

### 5. `ls` parsing in cleanup script

**File:** `scripts/home-snapshot-cleanup.sh:23`

`for snap in $(ls -1d ...)` is fragile. While the `home-YYYY-MM-DDTHH-MM-SS` format won't have spaces, this is still a bash antipattern.

**Fix:** Use a glob:

```bash
shopt -s nullglob
snapshots=("$SNAPSHOT_DIR"/home-*)
# sort by name (which is also by timestamp)
IFS=$'\n' sorted=($(printf '%s\n' "${snapshots[@]}" | sort -r))
unset IFS
```

Or simpler: just use the glob directly in the loop since `sort -r` is the real requirement:

```bash
for snap in "$SNAPSHOT_DIR"/home-*; do
    ...
done
```

Collect into array, sort, then iterate.

## Implementation Order

1. Fix retention logic (critical — data loss)
2. Add retry-on-failure to watcher
3. Fix `ls` parsing
4. Fix docs for service scope
5. Create `home-restore.sh`

## Testing

```bash
# Create test snapshots with specific timestamps
btrfs subvolume snapshot -r /home/lainos /snapshots/home-2026-05-16T00-00-00
btrfs subvolume snapshot -r /home/lainos /snapshots/home-2026-05-16T01-23-45
btrfs subvolume snapshot -r /home/lainos /snapshots/home-2026-05-16T02-15-30

# Run cleanup and verify retention
/usr/local/bin/home-snapshot-cleanup.sh
ls /snapshots/
```

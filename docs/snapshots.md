# Home Directory Snapshots

## Overview

LainOS automatically creates btrfs copy-on-write snapshots of `/home/lainos/` when files change. Snapshots are:
- **Instant** — COW means zero disk space for unchanged data
- **Atomic** — consistent point-in-time capture
- **Automatic** — triggered by file changes, debounced to a configurable interval
- **Self-managing** — tiered retention policy prunes old snapshots automatically

## Setup

### One-time migration

The `.data/home` directory must be a btrfs subvolume for snapshots to work. Run this script on the **host** (not inside the container):

```bash
./scripts/migrate-to-subvolume.sh
```

This script:
1. Stops the container
2. Converts `.data/home` to a btrfs subvolume
3. Creates the `.data/snapshots/` directory
4. Restarts the container

The migration is idempotent — safe to run multiple times.

### Verify

After migration, verify inside the container:

```bash
btrfs subvolume show /home/lainos
# Should print subvolume details

systemctl status home-snapshot
# Should show active (running)
```

## Configuration

Edit `/home/lainos/config/snapshot.conf`:

```bash
SNAPSHOT_INTERVAL=3600          # Min seconds between snapshots
SNAPSHOT_SOURCE="/home/lainos"  # Directory to snapshot
SNAPSHOT_DIR="/snapshots"       # Where snapshots are stored
SNAPSHOT_RETENTION_DAYS=30      # Max age in days
SNAPSHOT_EXCLUDE="\.cache|\.camofox|\.camoufox|\.npm|\.local|\.fontconfig|/tmp"
```

Apply changes:

```bash
systemctl restart home-snapshot
```

### Options

| Variable | Default | Description |
|---|---|---|
| `SNAPSHOT_INTERVAL` | `3600` | Minimum seconds between snapshots. Lower = more granular but more snapshots. |
| `SNAPSHOT_SOURCE` | `/home/lainos` | Btrfs subvolume to snapshot. Must be a subvolume. |
| `SNAPSHOT_DIR` | `/snapshots` | Directory for storing snapshots. Must be on same btrfs filesystem. |
| `SNAPSHOT_RETENTION_DAYS` | `30` | Maximum age of snapshots in days. Older snapshots are pruned. |
| `SNAPSHOT_EXCLUDE` | (regex) | Pipe-separated regex of paths to exclude from triggering snapshots. |

### Common configurations

**Every 5 minutes (testing):**
```bash
SNAPSHOT_INTERVAL=300
```

**Every 6 hours (low overhead):**
```bash
SNAPSHOT_INTERVAL=21600
```

**Keep 90 days:**
```bash
SNAPSHOT_RETENTION_DAYS=90
```

## Usage

### List snapshots

```bash
ls -la /snapshots/
# Output:
# dr-xr-xr-x. 1 root root 0 May  5 08:00 home-2026-05-05T08-00-00
# dr-xr-xr-x. 1 root root 0 May  5 09:00 home-2026-05-05T09-00-00
```

Or use the restore tool:

```bash
home-restore.sh --list
```

### Restore a file or directory

```bash
home-restore.sh --restore /snapshots/home-2026-05-05T08-00-00 path/to/file
```

This copies from the snapshot using `--reflink=auto` (zero-cost on btrfs).

### Restore everything (full restore)

> **Warning:** This stops all services, replaces the entire home directory contents, then restarts services. Active sessions, in-progress writes, and running databases will be interrupted. Use `--dry-run` first to preview changes.

```bash
home-restore.sh --full /snapshots/home-2026-05-05T08-00-00
```

Use `--dry-run` first to preview changes:

```bash
home-restore.sh --dry-run --full /snapshots/home-2026-05-05T08-00-00
```

### Compare current state vs snapshot

```bash
home-restore.sh --diff /snapshots/home-2026-05-05T08-00-00
```

Or manually:

```bash
diff -r /snapshots/home-2026-05-05T08-00-00/workspace ~/workspace
```

### Create a manual snapshot

```bash
/usr/local/bin/home-snapshot.sh
```

### Trigger cleanup immediately

```bash
systemctl start home-snapshot-cleanup
```

## Restore Tool

`home-restore.sh` provides safe snapshot restore operations:

| Command | Description |
|---|---|
| `--list` | List available snapshots with timestamps |
| `--diff <snapshot>` | Show changed files between snapshot and current state |
| `--restore <snapshot> <path>` | Restore a single file/directory |
| `--full <snapshot>` | Full restore: stop services, rsync, restart services |
| `--dry-run` | Preview any operation without making changes |

All destructive operations require confirmation unless `--yes` is passed.

### Examples

```bash
# List snapshots
home-restore.sh --list

# See what changed
home-restore.sh --diff /snapshots/home-2026-05-15T12-00-00

# Restore a single file
home-restore.sh --restore /snapshots/home-2026-05-15T12-00-00 .bashrc

# Restore a directory
home-restore.sh --restore /snapshots/home-2026-05-15T12-00-00 .config

# Full restore (stops services, restores everything, restarts)
home-restore.sh --full /snapshots/home-2026-05-15T12-00-00

# Preview a full restore
home-restore.sh --dry-run --full /snapshots/home-2026-05-15T12-00-00
```

## Retention Policy

Cleanup runs hourly and applies these tiers:

| Age | Policy |
|---|---|
| < 1 hour | Keep all |
| < 24 hours | Keep 1 per hour (closest to HH:00:00) |
| < 7 days | Keep 1 per day (closest to 00:00:00) |
| < 30 days | Keep 1 per week (closest to Sunday 00:00:00) |
| > 30 days | Delete |

To change retention period, set `SNAPSHOT_RETENTION_DAYS` in `snapshot.conf`.

## Excluded Directories

These directories do not trigger snapshot creation:

| Directory | Reason |
|---|---|
| `.cache/` | Browser and system cache — regenerable |
| `.camofox/` | Camofox session data — high churn |
| `.camoufox/` | Browser binary cache — regenerable |
| `.npm/` | npm package cache — regenerable |
| `.local/` | Nix profile links — regenerable via nix |
| `.fontconfig/` | Font cache — regenerable |
| `tmp/` | Temporary files |

These directories ARE still included in snapshots when a snapshot is taken — they just don't trigger new snapshots on their own. To exclude them from snapshots entirely, move them to a separate location outside the home subvolume.

## Service Management

The snapshot services run as **system services** (not user services). Use `systemctl` without `--user`:

```bash
# Check watcher status
systemctl status home-snapshot

# Restart watcher (after config change)
systemctl restart home-snapshot

# View watcher logs
journalctl -u home-snapshot -f

# Check cleanup timer schedule
systemctl list-timers home-snapshot-cleanup

# Trigger cleanup now
systemctl start home-snapshot-cleanup

# View cleanup logs
journalctl -u home-snapshot-cleanup -f
```

## Troubleshooting

### "Not a btrfs subvolume" error

Run the migration script on the host:
```bash
./scripts/migrate-to-subvolume.sh
```

### "Snapshots directory not found"

Create it and restart:
```bash
# On the host:
mkdir -p .data/snapshots
# Then rebuild/restart the container
podman-compose up -d --build
```

### Too many snapshots accumulating

Decrease the retention period or increase the interval:
```bash
# In ~/config/snapshot.conf:
SNAPSHOT_INTERVAL=7200           # every 2 hours
SNAPSHOT_RETENTION_DAYS=7        # keep only 7 days
systemctl restart home-snapshot
```

Then trigger immediate cleanup:
```bash
systemctl start home-snapshot-cleanup
```

### Snapshot watcher not running

Check service status and logs:
```bash
systemctl status home-snapshot
journalctl -u home-snapshot --no-pager -n 50
```

Common causes:
- `inotifywait` not found: rebuild container to install `inotify-tools`
- `/home/lainos` not a subvolume: run `migrate-to-subvolume.sh`
- Permissions issue: service may need to run as root

# Plan: Btrfs Snapshot System for /home/lainos

## Overview

Create an event-driven btrfs snapshot system that watches `/home/lainos/` for file changes and creates read-only, copy-on-write snapshots at a configurable interval (default: hourly). Includes tiered retention pruning and full runtime configurability from inside the container.

## Prerequisites

- Host filesystem is **btrfs** (confirmed: `/dev/nvme0n1p3 btrfs` at `/var/home/lvturner`)
- `.data/home` must be converted to a btrfs subvolume (one-time migration)
- Container is privileged (has btrfs ioctl access)

## Architecture

```
Host filesystem (btrfs)
├── .data/home/              ← btrfs subvolume (bind-mounted as /home/lainos)
├── .data/snapshots/         ← snapshot storage (bind-mounted as /snapshots)
│   ├── home-2026-05-05T08-00-00/   ← read-only btrfs snapshots
│   ├── home-2026-05-05T09-00-00/
│   └── ...
```

Data flow:

```
inotifywait (watcher)
  → detects change in /home/lainos/
  → checks SNAPSHOT_INTERVAL elapsed since last snapshot
  → if yes: btrfs subvolume snapshot -r /home/lainos /snapshots/home-<timestamp>
  → if no: skip (debounced)

Cleanup timer (hourly)
  → lists snapshots in /snapshots/
  → applies retention tiers
  → deletes expired snapshots via btrfs subvolume delete
```

## Configuration

**File:** `/home/lainos/config/snapshot.conf` (template: `config/snapshot.conf.example`)

```bash
SNAPSHOT_INTERVAL=3600          # min seconds between snapshots (default: hourly)
SNAPSHOT_SOURCE="/home/lainos"  # directory to snapshot
SNAPSHOT_DIR="/snapshots"       # where snapshots are stored
SNAPSHOT_RETENTION_DAYS=30      # max age in days
SNAPSHOT_EXCLUDE="\.cache|\.camofox|\.camoufox|\.npm|\.local|\.fontconfig|/tmp"
```

Changes take effect on `systemctl --user restart home-snapshot`.

## Retention Policy

| Age | Keep |
|---|---|
| < 1 hour | All snapshots |
| < 24 hours | 1 per hour (keep HH:00) |
| < 7 days | 1 per day (keep midnight) |
| < 30 days | 1 per week (keep Sunday midnight) |
| > 30 days | Delete |

## Excluded Directories

These directories are excluded from triggering snapshots (high churn, regenerable data):

| Directory | Reason |
|---|---|
| `.cache/` | Browser cache, fontconfig cache — regenerable |
| `.camofox/` | Camofox session data — high churn |
| `.camoufox/` | Browser binary cache — regenerable |
| `.npm/` | npm package cache — regenerable |
| `.local/` | Nix profile links — regenerable via nix |
| `.fontconfig/` | Font cache — regenerable |
| `tmp/` | Temporary files |

## Files to Create (9)

### 1. `scripts/migrate-to-subvolume.sh`

One-time host-side migration script. Run on the host, not inside the container.

**Steps:**
1. Verify host filesystem is btrfs (`stat -f -c '%T' .data/home`)
2. Verify `.data/home` is not already a subvolume (`btrfs subvolume show .data/home`)
3. Stop the container (`podman-compose down`)
4. Move `.data/home` → `.data/home_migrate`
5. Create btrfs subvolume: `btrfs subvolume create .data/home`
6. Copy contents: `cp -a --reflink=auto .data/home_migrate/. .data/home/`
7. Remove old: `rm -rf .data/home_migrate`
8. Create snapshots dir: `mkdir -p .data/snapshots`
9. Print success message with next steps

**Safety:**
- Exit 0 if already a subvolume (idempotent)
- Exit 1 if not on btrfs
- Preserve all permissions and ownership
- Don't proceed if container is running (check `podman ps`)

### 2. `scripts/home-snapshot.sh`

Creates a single read-only btrfs snapshot. Called by the watcher and available for manual use.

**Logic:**
1. Source `/home/lainos/config/snapshot.conf` (fallback to defaults)
2. Verify `$SNAPSHOT_SOURCE` is a btrfs subvolume (`btrfs subvolume show`)
3. Get current btrfs generation of source (`btrfs subvolume show $SNAPSHOT_SOURCE | grep 'Generation:'`)
4. Get generation of last snapshot (if any)
5. If generation unchanged since last snapshot, exit silently (no changes)
6. Create read-only snapshot: `btrfs subvolume snapshot -r $SNAPSHOT_SOURCE $SNAPSHOT_DIR/home-$(date +%Y-%m-%dT%H-%M-%S)`
7. Log result

**Error handling:**
- Exit 1 if not a subvolume (log helpful message about migrate-to-subvolume.sh)
- Exit 1 if snapshot dir doesn't exist or isn't on same btrfs filesystem

### 3. `scripts/home-snapshot-watch.sh`

Long-running watcher process. Runs as the main process of `home-snapshot.service`.

**Logic:**
1. Source config (with defaults fallback)
2. Validate prerequisites (subvolume exists, snapshot dir exists, inotifywait available)
3. Record `last_snapshot=0` (epoch)
4. Start `inotifywait -m -r -e modify,create,delete,moved_to,moved_from --exclude "$SNAPSHOT_EXCLUDE" "$SNAPSHOT_SOURCE"`
5. Pipe to while loop:
   a. On each event, get `now=$(date +%s)`
   b. Calculate `elapsed = now - last_snapshot`
   c. If `elapsed >= SNAPSHOT_INTERVAL`:
      - Call `/usr/local/bin/home-snapshot.sh`
      - Update `last_snapshot=$now`
   d. If `elapsed < SNAPSHOT_INTERVAL`: skip (debounced)
6. If inotifywait exits (e.g., directory deleted), log error and exit

**Note:** The `last_snapshot` variable persists within the pipe subshell, so the debounce works correctly within a single invocation.

### 4. `scripts/home-snapshot-cleanup.sh`

Prunes old snapshots based on retention tiers. Called by `home-snapshot-cleanup.timer`.

**Logic:**
1. Source config (with defaults fallback)
2. List all snapshots in `$SNAPSHOT_DIR` matching pattern `home-*`
3. Sort by timestamp (newest first)
4. For each snapshot:
   a. Parse timestamp from directory name
   b. Calculate age in seconds
   c. Apply retention tier:
      - `< 3600s` (1 hour): KEEP
      - `< 86400s` (1 day): keep only if minute==0 and second==0 (top of hour)
      - `< 604800s` (7 days): keep only if hour==0 and minute==0 (midnight)
      - `< 2592000s` (30 days): keep only if day-of-week==Sunday and hour==0
      - `>= 2592000s`: DELETE
5. Delete expired: `btrfs subvolume delete $SNAPSHOT_DIR/$expired_snapshot`
6. Log count of kept/deleted snapshots

### 5. `systemd/home-snapshot.service`

```ini
[Unit]
Description=Home directory btrfs snapshot watcher
After=home-mount.service fix-home-permissions.service
Wants=home-snapshot-cleanup.timer

[Service]
Type=simple
ExecStart=/usr/local/bin/home-snapshot-watch.sh
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

**Notes:**
- `After=fix-home-permissions.service` ensures home dir is ready
- `Restart=on-failure` with backoff handles transient errors
- Runs as root (needs btrfs ioctl access) — or verify if lainos user can create snapshots

### 6. `systemd/home-snapshot-cleanup.service`

```ini
[Unit]
Description=Prune old home directory snapshots

[Service]
Type=oneshot
ExecStart=/usr/local/bin/home-snapshot-cleanup.sh
```

### 7. `systemd/home-snapshot-cleanup.timer`

```ini
[Unit]
Description=Hourly snapshot cleanup

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

### 8. `config/snapshot.conf.example`

Template config file with all options and comments:

```bash
# Snapshot configuration — edit and restart: systemctl --user restart home-snapshot
SNAPSHOT_INTERVAL=3600
SNAPSHOT_SOURCE="/home/lainos"
SNAPSHOT_DIR="/snapshots"
SNAPSHOT_RETENTION_DAYS=30
SNAPSHOT_EXCLUDE="\.cache|\.camofox|\.camoufox|\.npm|\.local|\.fontconfig|/tmp"
```

Copied to `/home/lainos/config/snapshot.conf` on first run (via `start.sh` sync_config).

### 9. `docs/snapshots.md`

Full user-facing documentation page. See "Documentation Content" section below.

## Files to Modify (8)

### 10. `compose.yaml`

Add snapshots volume mount:

```yaml
volumes:
  - ./.data/home:/home/lainos:Z
  - ./.data/snapshots:/snapshots:Z    # ADD THIS
  - nix:/nix
```

### 11. `Containerfile`

Two changes:

1. Add `inotify-tools` to the `rpm-ostree install` block (first one, with nodejs/curl/git):
   ```
   rpm-ostree install \
       nodejs \
       curl \
       git unzip python3 \
       inotify-tools \          # ADD THIS
       gtk3 dbus-glib ...
   ```

2. Add new script/unit copies (after existing COPY lines for scripts/systemd):
   ```
   COPY scripts/home-snapshot.sh /usr/local/bin/home-snapshot.sh
   COPY scripts/home-snapshot-watch.sh /usr/local/bin/home-snapshot-watch.sh
   COPY scripts/home-snapshot-cleanup.sh /usr/local/bin/home-snapshot-cleanup.sh
   COPY systemd/home-snapshot.service /usr/lib/systemd/system/
   COPY systemd/home-snapshot-cleanup.service /usr/lib/systemd/system/
   COPY systemd/home-snapshot-cleanup.timer /usr/lib/systemd/system/
   RUN chmod +x /usr/local/bin/home-snapshot.sh /usr/local/bin/home-snapshot-watch.sh /usr/local/bin/home-snapshot-cleanup.sh
   ```

### 12. `systemd/98-lainos.preset`

Add new units to the enable list:

```
enable home-snapshot.service
enable home-snapshot-cleanup.timer
```

### 13. `README.md`

Changes:
- Add `**[Snapshots](docs/snapshots.md)** — Automatic btrfs snapshots of the home directory, triggered by file changes with tiered retention` to the "What's included" bullet list (after camofox)
- Add new `## Snapshots` section after `## Container Management`:
  ```markdown
  ## Snapshots

  The home directory is snapshotted automatically using btrfs copy-on-write snapshots. Snapshots are triggered by file changes (debounced to a configurable interval, default: hourly) and pruned automatically with a tiered retention policy (30 days). Restore any file or directory from a snapshot using standard file copy.

  See **[docs/snapshots.md](docs/snapshots.md)** for setup, configuration, restore, and management.
  ```
- Update project structure to add `.data/snapshots/ → Home directory snapshots (bind-mounted)`

### 14. `AGENTS.md`

Changes:
- Add `, automatic btrfs snapshots of the home directory` to Project Summary sentence
- Add `- **Snapshot tooling**: inotify-tools, btrfs-progs` to Tech Stack
- Add new section after `## Camofox`:

  ```markdown
  ## Snapshot System

  The home directory (`/home/lainos`) is a btrfs subvolume, automatically snapshotted when files change. Snapshots are read-only btrfs COW snapshots stored in `/snapshots/`.

  - **Config**: `/home/lainos/config/snapshot.conf` (sourced shell vars, reconfigurable at runtime)
  - **Watcher**: `home-snapshot.service` runs `home-snapshot-watch.sh` (inotifywait + debounce)
  - **Cleanup**: `home-snapshot-cleanup.timer` runs hourly, applies tiered retention (30 days)
  - **Snapshot scripts**: `scripts/home-snapshot.sh`, `scripts/home-snapshot-watch.sh`, `scripts/home-snapshot-cleanup.sh`
  - **One-time migration**: `scripts/migrate-to-subvolume.sh` (run on host before first use)

  ### Key config variables

  | Variable | Default | Description |
  |---|---|---|
  | `SNAPSHOT_INTERVAL` | `3600` | Min seconds between snapshots |
  | `SNAPSHOT_SOURCE` | `/home/lainos` | Directory to snapshot |
  | `SNAPSHOT_DIR` | `/snapshots` | Snapshot storage location |
  | `SNAPSHOT_RETENTION_DAYS` | `30` | Max snapshot age in days |
  | `SNAPSHOT_EXCLUDE` | (regex) | inotifywait exclude patterns |

  Changes require `systemctl --user restart home-snapshot`.
  ```

- Update Volumes table to add:

  ```
  | `./.data/snapshots/` | `/snapshots` | Btrfs snapshot storage |
  ```

- Add to file conventions: `- Snapshot config uses `.conf` suffix in `config/` (sourced as shell variables)`

### 15. `docs/README.md`

Add to Contents list:

```markdown
- **[snapshots.md](snapshots.md)** — Home directory snapshots: setup, configuration, restore, retention policy, and troubleshooting
```

### 16. `docs/container-management.md`

Add to Volume Mounts table:

```
| `./.data/snapshots/` | `/snapshots` | Btrfs snapshot storage |
```

Add new section before `## Clean Rebuild`:

```markdown
## Snapshots

The home directory is automatically snapshotted using btrfs when files change. Requires a one-time migration to convert `.data/home` to a btrfs subvolume (see [snapshots.md](snapshots.md)).

```bash
# List snapshots
ls /snapshots/

# Restore a file from a snapshot
cp -a --reflink=auto /snapshots/home-2026-05-05T08-00-00/some-file ~/some-file

# Restore a directory
cp -a --reflink=auto /snapshots/home-2026-05-05T08-00-00/.config ~/.config

# Manual snapshot
/usr/local/bin/home-snapshot.sh

# Change snapshot interval
vim ~/config/snapshot.conf
systemctl --user restart home-snapshot

# Trigger cleanup immediately
systemctl --user start home-snapshot-cleanup
```

See **[snapshots.md](snapshots.md)** for full setup, configuration, and troubleshooting.
```

### 17. `config/agents.example.md`

Add new section before `## No sudo`:

```markdown
## Home Directory Snapshots

The home directory is automatically snapshotted using btrfs copy-on-write snapshots. Snapshots are stored in `/snapshots/` and named by timestamp (e.g., `home-2026-05-05T08-00-00`).

### Configuration

Edit `/home/lainos/config/snapshot.conf` and restart the watcher:

```bash
vim ~/config/snapshot.conf
systemctl --user restart home-snapshot
```

Key settings:
- `SNAPSHOT_INTERVAL` — min seconds between snapshots (default: 3600 / hourly)
- `SNAPSHOT_RETENTION_DAYS` — how long to keep snapshots (default: 30 days)
- `SNAPSHOT_EXCLUDE` — regex of paths to exclude from triggering snapshots

### Restoring files

Snapshots are read-only directory trees. Copy files out using `--reflink=auto` (zero-cost on btrfs):

```bash
# List available snapshots
ls /snapshots/

# Restore a single file
cp -a --reflink=auto /snapshots/home-2026-05-05T08-00-00/path/to/file ~/path/to/file

# Restore a directory
cp -a --reflink=auto /snapshots/home-2026-05-05T08-00-00/.config ~/.config

# Compare current vs snapshot
diff -r /snapshots/home-2026-05-05T08-00-00/workspace ~/workspace
```

### Manual operations

```bash
# Create a snapshot now
/usr/local/bin/home-snapshot.sh

# Trigger cleanup immediately
systemctl --user start home-snapshot-cleanup

# View snapshot service logs
journalctl --user -u home-snapshot -f
```

### Retention policy

- All snapshots kept for 1 hour
- 1 per hour for 24 hours
- 1 per day for 7 days
- 1 per week for 30 days
- Older than 30 days: deleted

### Excluded directories

These directories do not trigger snapshots (high churn, regenerable):
`.cache`, `.camofox`, `.camoufox`, `.npm`, `.local`, `.fontconfig`, `tmp`
```

## Documentation Content: `docs/snapshots.md`

```markdown
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

systemctl --user status home-snapshot
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
systemctl --user restart home-snapshot
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

### Restore a file

```bash
cp -a --reflink=auto /snapshots/home-2026-05-05T08-00-00/path/to/file ~/path/to/file
```

### Restore a directory

```bash
cp -a --reflink=auto /snapshots/home-2026-05-05T08-00-00/.config ~/.config
```

### Restore everything (nuclear option)

```bash
# From inside the container, as root:
rsync -a --delete /snapshots/home-2026-05-05T08-00-00/ /home/lainos/
```

### Compare current state vs snapshot

```bash
diff -r /snapshots/home-2026-05-05T08-00-00/workspace ~/workspace
```

### Create a manual snapshot

```bash
/usr/local/bin/home-snapshot.sh
```

### Trigger cleanup immediately

```bash
systemctl --user start home-snapshot-cleanup
```

## Retention Policy

Cleanup runs hourly and applies these tiers:

| Age | Policy |
|---|---|
| < 1 hour | Keep all |
| < 24 hours | Keep 1 per hour (top of hour) |
| < 7 days | Keep 1 per day (midnight) |
| < 30 days | Keep 1 per week (Sunday midnight) |
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

```bash
# Check watcher status
systemctl --user status home-snapshot

# Restart watcher (after config change)
systemctl --user restart home-snapshot

# View watcher logs
journalctl --user -u home-snapshot -f

# Check cleanup timer schedule
systemctl --user list-timers home-snapshot-cleanup

# Trigger cleanup now
systemctl --user start home-snapshot-cleanup

# View cleanup logs
journalctl --user -u home-snapshot-cleanup -f
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
systemctl --user restart home-snapshot
```

Then trigger immediate cleanup:
```bash
systemctl --user start home-snapshot-cleanup
```

### Snapshot watcher not running

Check service status and logs:
```bash
systemctl --user status home-snapshot
journalctl --user -u home-snapshot --no-pager -n 50
```

Common causes:
- `inotifywait` not found: rebuild container to install `inotify-tools`
- `/home/lainos` not a subvolume: run `migrate-to-subvolume.sh`
- Permissions issue: service may need to run as root
```

## Implementation Order

1. Create `scripts/migrate-to-subvolume.sh`
2. Create `scripts/home-snapshot.sh`
3. Create `scripts/home-snapshot-watch.sh`
4. Create `scripts/home-snapshot-cleanup.sh`
5. Create `config/snapshot.conf.example`
6. Create systemd units (service + service + timer)
7. Modify `systemd/98-lainos.preset`
8. Modify `compose.yaml` (add snapshots volume)
9. Modify `Containerfile` (install inotify-tools, copy scripts/units)
10. Create `docs/snapshots.md`
11. Modify `docs/README.md`
12. Modify `docs/container-management.md`
13. Modify `README.md`
14. Modify `AGENTS.md`
15. Modify `config/agents.example.md`

## Testing Checklist

After implementation, verify:

1. **Migration**: Run `migrate-to-subvolume.sh` on host, confirm `.data/home` is a subvolume
2. **Container build**: `podman-compose up -d --build` succeeds
3. **Service starts**: `systemctl --user status home-snapshot` shows active
4. **Snapshot on change**: Create a file in `/home/lainos/`, wait for interval, confirm snapshot appears
5. **Debounce**: Create multiple files rapidly, confirm only one snapshot created
6. **Exclude**: Modify a file in `.cache/`, confirm no snapshot triggered
7. **Manual snapshot**: Run `/usr/local/bin/home-snapshot.sh`, confirm snapshot created
8. **Restore**: Copy a file from a snapshot, confirm content matches
9. **Cleanup**: Create snapshots, wait for cleanup timer or trigger manually, confirm retention policy applied
10. **Config change**: Change `SNAPSHOT_INTERVAL` to 60, restart service, confirm new interval respected
11. **Service restart**: `systemctl --user restart home-snapshot`, confirm it comes back up
12. **Build verification**: `cd gateway && go build ./...` and `cd lain && go build ./...` still pass

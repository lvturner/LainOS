# Container Management

## Build & Start

```bash
# Build and start (prompts for lain profile setup on first run)
./start.sh

# Or manually:
podman-compose build
podman-compose up -d

# Rebuild after code changes (Gateway or lain source)
podman-compose up -d --build
```

## Stop & Restart

```bash
# Stop the container
podman-compose down

# Restart without rebuilding
podman-compose restart
```

## Logs

```bash
# View logs
podman logs lainos

# Follow logs
podman logs -f lainos

# First 10 lines include the auto-generated SSH password
podman logs lainos | head -10
```

## SSH Access

The container generates a random password for the `lainos` user on first boot. The password is printed to stdout during setup.

```bash
# Get the password
podman logs lainos | head -10

# Connect
ssh -p 2222 lainos@localhost
```

The `lainos` user's login shell is `bash`. After logging in, the `/etc/motd` banner shows how to start `lain`.

To launch lain directly:

```bash
ssh -p 2222 lainos@localhost -t lain
```

## Systemd Services Inside the Container

Services are managed by systemd (PID 1 inside the container). All services start automatically on boot.

```bash
# Restart the gateway service (after manual config changes or troubleshooting)
systemctl --user restart wh-gateway

# Restart cloudflared (after changing cloudflared.yaml)
systemctl --user restart cloudflared

# Restart camofox
systemctl --user restart camofox

# Check service status
systemctl --user status wh-gateway
systemctl --user status cloudflared
systemctl --user status camofox

# View service logs
journalctl --user -u wh-gateway -f
journalctl --user -u camofox -f
```

Note: `gateway.yaml` changes are hot-reloaded — you don't need to restart the gateway service for config updates. Only restart if the binary itself changed.

## Volume Mounts

| Host mount | Container path | Purpose |
|---|---|---|
| `./.data/home/` | `/home/lainos/` | Entire home directory (config, workspace, dotfiles) |
| `./.data/snapshots/` | `/snapshots` | Btrfs snapshot storage |
| `nix` (named volume) | `/nix` | Nix store |

The `:Z` SELinux label is required on bind mounts for podman — this is already configured in `compose.yaml`.

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
systemctl restart home-snapshot

# Trigger cleanup immediately
systemctl start home-snapshot-cleanup
```

See **[snapshots.md](snapshots.md)** for full setup, configuration, and troubleshooting.

## Clean Rebuild

To start completely fresh:

```bash
podman-compose down
podman-compose build --no-cache
podman-compose up -d
```

This will regenerate the SSH password and lose any state not on bind-mounted volumes. Your `.data/` directory is preserved since it's bind-mounted from the host.

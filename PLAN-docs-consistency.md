# Plan: Documentation Consistency

## Problem

Several docs reference wrong commands, wrong scopes, or are inconsistent with the actual implementation.

## Issues

### 1. Snapshot docs use `systemctl --user` for a system service

**Files:** `docs/snapshots.md`, `config/agents.example.md`, `AGENTS.md`

All docs say `systemctl --user restart home-snapshot` but the service is at `/usr/lib/systemd/system/home-snapshot.service` (system scope). Running `systemctl --user` won't find it.

**Fix:** Replace all `systemctl --user` references for snapshot services with `systemctl` (system scope). Also update the note about running as root since the service has no `User=` directive.

Alternatively, if moving the service to user scope (requires root for btrfs), update the service file and preset instead.

### 2. `docs/snapshots.md` restore section is dangerous

**File:** `docs/snapshots.md:109-114`

The "nuclear option" restore runs `rsync -a --delete` into `/home/lainos/` while services are running. This can corrupt active sessions, partially-written config files, and running databases.

**Fix:** Add prominent warnings:

```markdown
### Restore everything (nuclear option)

> **Warning:** Stop all services before full restore to avoid corruption.
> This replaces the entire home directory contents.

```bash
# Stop services first
systemctl stop wh-gateway camofox cloudflared

# Restore
rsync -a --delete /snapshots/home-2026-05-05T08-00-00/ /home/lainos/

# Restart services
systemctl start wh-gateway camofox cloudflared
```
```

### 3. AGENTS.md references `PLAN.md` which doesn't exist

**File:** `AGENTS.md:4`

> Full plan lives in `PLAN.md`.

`PLAN.md` does not exist. There are 7 `PLAN-*.md` files instead.

**Fix:** Update to reference the actual plan files, or remove the line.

### 4. Gateway default config path mismatch

**File:** `gateway/main.go:13`

The default config path is `/config/gateway.yaml` but:
- The systemd unit uses `/home/lainos/config/gateway.yaml`
- The example config copies to `.data/home/config/gateway.yaml`
- The Containerfile mounts `.data/home/` to `/home/lainos/`

The default `/config/gateway.yaml` only works if a `/config` mount exists, which it doesn't in `compose.yaml`. This isn't a bug (the systemd unit overrides the flag), but the default is misleading.

**Fix:** Change the default to `/home/lainos/config/gateway.yaml`:

```go
configPath := flag.String("config", "/home/lainos/config/gateway.yaml", "path to config file")
```

### 5. `config/snapshot.conf.example` documents `SNAPSHOT_RETENTION_DAYS` but cleanup uses multiple tiers

**File:** `config/snapshot.conf.example`

The example shows `SNAPSHOT_RETENTION_DAYS=30` implying a simple "delete after 30 days" policy. But the cleanup script has 4 tiers with different granularity. Users may not understand the actual behavior.

**Fix:** Add comments to the example config:

```bash
# Retention policy (applied hourly by cleanup timer):
#   < 1 hour: keep all
#   < 24 hours: keep 1 per hour
#   < 7 days: keep 1 per day  
#   < SNAPSHOT_RETENTION_DAYS: keep 1 per week
#   > SNAPSHOT_RETENTION_DAYS: delete
SNAPSHOT_RETENTION_DAYS=30
```

### 6. Wh-gateway service management in agents.md says `systemctl restart wh-gateway`

**File:** `config/agents.example.md`

The doc says `systemctl restart wh-gateway` without `--user`, but `wh-gateway.service` is installed at `/etc/systemd/user/` (user scope). It should be `systemctl --user restart wh-gateway`.

**Fix:** Check actual service scope and update docs consistently:
- `wh-gateway.service`, `camofox.service`, `cloudflared.service` → user services → `systemctl --user`
- `home-snapshot.service`, `home-snapshot-cleanup.*` → system services → `systemctl`

## Implementation Order

1. Fix `systemctl --user` vs system scope across all docs
2. Fix nuclear restore warning
3. Fix `PLAN.md` reference
4. Fix gateway default config path
5. Improve `snapshot.conf.example` comments
6. Fix wh-gateway service scope in agents.md

## Testing

```bash
# Verify no stale references
grep -r "systemctl --user.*home-snapshot" docs/ config/ AGENTS.md
# Should return nothing

grep -r "systemctl.*wh-gateway" docs/ config/
# Should all use --user scope

grep -r "PLAN\.md" AGENTS.md
# Should not reference non-existent file
```

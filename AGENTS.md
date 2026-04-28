# AGENTS.md — lainos project conventions

## Project Summary

Webhook HTTP gateway written in Go, running as a single privileged systemd container on ucore-minimal (Fedora CoreOS). Routes incoming webhooks to user-defined commands. Cloudflared tunnels external traffic in. Config is YAML with hot-reload. Full plan lives in `PLAN.md`.

## Tech Stack

- **Language**: Go (module lives in `gateway/`)
- **Container runtime**: podman (docker compatibility layer available)
- **Base image**: `ghcr.io/ublue-os/ucore-minimal:stable`
- **Process manager**: systemd (PID 1 in container)
- **Config format**: YAML (`gopkg.in/yaml.v3`)
- **Config hot-reload**: fsnotify (`github.com/fsnotify/fsnotify`)
- **Container orchestration**: podman-compose via `compose.yaml`

## Build & Run Commands

```bash
# Build and start (prompts for username on first run)
./start.sh

# Build the container image
podman-compose build

# Start the container
podman-compose up -d

# Rebuild after code changes
podman-compose up -d --build

# View logs (includes SSH password on first boot)
podman logs lainos

# Stop
podman-compose down
```

## Go Application

The Go source lives in `gateway/`. All `.go` files are in a single package (`main`) at the directory root.

### Dependencies

- `gopkg.in/yaml.v3`
- `github.com/fsnotify/fsnotify`

### Key modules (one file each)

| File | Responsibility |
|---|---|
| `main.go` | Entry point, flag parsing, wiring |
| `config.go` | YAML structs, `LoadConfig()` |
| `watcher.go` | fsnotify config hot-reload (debounced 500ms) |
| `server.go` | HTTP server, dynamic route registration, atomic mux swap |
| `handler.go` | Command execution: body→stdin, headers→`WH_HEADER_*` env vars |

### Environment variables set for commands

- `WH_METHOD` — HTTP method
- `WH_PATH` — request path
- `WH_QUERY` — raw query string
- `WH_HEADER_<UPPERCASED>` — each HTTP header (dashes → underscores)

### Response mapping

- Command exit 0 → HTTP 200 with stdout
- Command exit non-zero → HTTP 500 with stderr
- Timeout → HTTP 504

## Container Architecture

- Single container (`lainos`), systemd as PID 1
- **NEVER** use `docker stop $(docker ps -aq)` — stops everything
- Use `podman-compose` for lifecycle management
- User `lainos` created at build time with `/bin/bash` as login shell; `lain` is started via `/etc/motd` instructions or `shell.sh`
- Restart individual services inside the container via SSH:
  ```bash
  ssh -p 2222 lainos@localhost
  systemctl restart wh-gateway
  ```
- Container uses `stop_signal: SIGRTMIN+3` for clean systemd shutdown

## Volumes

| Host mount | Container path | Purpose |
|---|---|---|
| `./config/` | — | Tracked example templates (not mounted) |
| `./.data/config/` | `/home/lainos/config/` | `gateway.yaml`, `cloudflared.yaml`, cloudflared credentials |
| `./.data/workspace/` | `/home/lainos/workspace/` | User scripts executed by webhook routes |
| `./.data/lain/` | `/home/lainos/.config/lain/` | Lain profile config |
| `./.data/local/` | `/home/lainos/.local/` | Nix profiles, local binaries |
| *(named volume)* | `/nix` | Nix store — persists across container rebuilds |

- Config changes to `gateway.yaml` are hot-reloaded (no restart needed)
- Runtime data lives in `.data/` (gitignored); tracked templates live in `config/`
- The `:Z` SELinux label is required on volume mounts for podman

## Nix Package Management

The container has Nix installed for the `lainos` user (single-user mode, no daemon). Use it to install tools that handler scripts or the lain agent need. There is no `sudo` — Nix is the only way to install software.

```bash
# Install a package
nix profile install nixpkgs#jq

# List installed packages
nix profile list

# Remove a package by name
nix profile remove jq

# Free disk space from old packages
nix-collect-garbage
```

Installed binaries are immediately available in PATH. Packages persist across container rebuilds via the `/nix` named volume and `~/.local` bind mount.

## Code Style

- No comments unless explicitly asked
- Follow existing Go conventions in the codebase
- Keep all gateway source in `gateway/` as a flat `main` package
- Use `log/slog` for structured logging
- Use standard library HTTP types (`net/http`)

## Testing

After modifying Go code, always verify:
```bash
cd gateway && go build ./...
```

If test files exist:
```bash
cd gateway && go test ./...
```

## Documentation

User-facing documentation lives in `docs/` and is available inside the container at `/home/lainos/docs/`. When working on tasks, consult the docs folder for information about the gateway, lain, container management, cloudflare setup, webhook handler examples, and configuration. Use grep/glob to search `docs/` for relevant keywords before asking the user for help.

## File conventions

- Build file is `Containerfile` (not `Dockerfile`) — podman naming
- Compose file is `compose.yaml` (not `docker-compose.yml`)
- Example configs use `.example.yaml` / `.example.md` suffix in `config/`
- Systemd unit files go in `systemd/`
- Shell scripts go in `scripts/`
- Systemd presets go in `systemd/` as `98-lainos.preset` (must sort before `99-default-disable.preset`)

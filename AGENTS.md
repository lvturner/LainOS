# AGENTS.md — lainos project conventions

## Project Summary

Webhook HTTP gateway written in Go, running as a single privileged systemd container on ucore-minimal (Fedora CoreOS). Routes incoming webhooks to user-defined commands. Cloudflared tunnels external traffic in. Camofox provides an anti-detection headless browser server for AI agents. Automatic btrfs snapshots of the home directory. Config is YAML with hot-reload. Full plan lives in `PLAN.md`.

## Tech Stack

- **Language**: Go (modules in `gateway/` and `lain/`)
- **Plugin language**: Lua 5.1 via gopher-lua (`github.com/yuin/gopher-lua`)
- **Runtime**: Node.js 20 (via nix, for camofox)
- **Container runtime**: podman (docker compatibility layer available)
- **Base image**: `ghcr.io/ublue-os/ucore-minimal:stable`
- **Process manager**: systemd (PID 1 in container)
- **Config format**: YAML (`gopkg.in/yaml.v3`)
- **Config hot-reload**: fsnotify (`github.com/fsnotify/fsnotify`)
- **Container orchestration**: podman-compose via `compose.yaml`
- **Browser automation**: Camofox (Camoufox-based anti-detection browser, REST API on port 9377)
- **Snapshot tooling**: inotify-tools, btrfs-progs
- **TUI framework**: Bubble Tea + lipgloss (lain)

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

## Lain Application

The lain interactive LLM CLI lives in `lain/`. All `.go` files are in a single package (`main`) at the directory root.

### Dependencies

- `github.com/yuin/gopher-lua` — Lua 5.1 VM, pure Go, no CGO
- `github.com/fsnotify/fsnotify` — plugin directory hot-reload

### Key modules

| File | Responsibility |
|---|---|
| `main.go` | Entry point, flag parsing, wiring |
| `tui.go` | Bubble Tea model, mode system (insert/normal), key routing, slash commands |
| `window.go` | `Window` interface, `chatWindow` (viewport+messages), `todoWindow` (sidebar), `pluginWindow` (Lua callbacks) |
| `wm.go` | `WindowManager`: layout rendering, focus cycling, resize, window add/remove |
| `tile.go` | Binary layout tree: `SplitNode`/`LeafNode`, split/unsplit/resize, fixed-size splits |
| `float.go` | Floating window overlay layer with z-order and ANSI positioning |
| `plugin.go` | `PluginLoader`: Lua 5.1 VMs (full standard library), fsnotify hot-reload (debounced 500ms) |
| `plugin_api.go` | `PluginAPI`: full `lain.*` Lua API surface (window, chat, session, state, log, command, keybind, exec) |
| `llm.go` | LLM client, streaming, agentic loop, tool execution, idle watchdog |
| `tools.go` | Built-in tools: `run_command`, `ask_question`, `extend_timeout`, `todo` |
| `mcp.go` | MCP server manager (stdio JSON-RPC) |
| `compaction.go` | Context compaction (automatic + manual) |
| `config.go` | Profile config loading (YAML + JSON + markdown) |
| `profile.go` | Profile directory resolution |
| `session.go` | Session persistence (markdown frontmatter), title generation |
| `session_picker.go` | Session picker overlay (filter, preview) |
| `question.go` | Question modal (options, multiple choice, custom input) |
| `todo.go` | Task list persistence (JSON) |
| `markdown.go` | Markdown rendering via glamour |
| `banner.go` | ASCII banner |
| `oneshot.go` | Non-interactive mode |
| `sub_agent.go` | Sub-agent spawner: manages independent LLMClient + ToolRegistry + context lifecycle |
| `agent_window.go` | `agentWindow` type: mini-chat Window implementation for sub-agents |
| `notification.go` | Floating notification overlay: auto-dismissing, non-modal |

### Window interface

All UI panels implement `Window`:

```go
type Window interface {
    ID() string
    Title() string
    Update(tea.Msg) (Window, tea.Cmd)
    View(width, height int, focused bool) string
    SetSize(width, height int)
}
```

### Mode system

| Mode | Trigger | Keys go to |
|---|---|---|
| Insert | Default, `i` from normal | textarea (chat input) |
| Normal | `Ctrl+W` prefix | window manager commands |

### Plugin system

- Plugins are `.lua` files in `~/.config/lain/plugins/`
- Each plugin runs in a Lua 5.1 state with the full standard library (`base`, `string`, `table`, `math`, `coroutine`, `io`, `os`, `debug`, `package`)
- fsnotify watches for changes — plugins hot-reload without restart
- Plugins register windows, callbacks, slash commands, and keybindings via the `lain.*` API
- All Lua execution happens on the Bubble Tea update goroutine (thread-safe)

#### Plugin auto-start

Control which plugins load automatically on launch via `config.yaml`:

```yaml
plugins:
  enabled: ["weather", "clock"]  # only these auto-load; omit or empty = all plugins load
  render_timeout: 50ms
  callback_timeout: 5s
  load_timeout: 10s
```

- If `plugins.enabled` is absent or empty, **all** `.lua` files in the plugins directory load on start (backward compatible)
- The file watcher monitors the entire directory regardless of the enabled list, so newly-added files are still detected
- Slash commands for manual control:
  - `/plugins start` — load all plugins
  - `/plugins start <name>` — load a specific plugin
  - `/plugins stop <name>` — unload a specific plugin
  - `/plugins reload [name]` — reload one or all plugins
  - `/plugins` — list loaded plugins

### Sub-Agent System

Sub-agents are independent LLM clients running in their own tiled windows. They share the same
WindowManager but have isolated conversation history, tools, and cancellation.

- Spawned via `SubAgentManager.Spawn()` with configurable profile and system prompt
- Each gets its own `LLMClient`, `ToolRegistry`, and `MCPManager`
- Events routed via `subAgentEventMsg` to the correct `agentWindow`
- Window stays open after completion for user review

### Plugin Error Recovery

When a plugin fails to load or execute:
1. Error is captured via `PluginLoader.errCh` (not just logged)
2. User is prompted (normal mode: F to fix, Esc to dismiss) unless `auto_fix` is enabled
3. Fix agent spawned in a tiled window with the error context and plugin source
4. Agent reads/edits the Lua file, hot-reload picks up changes
5. On successful reload, agent is notified and a success notification is shown

Configuration in profile's `config.yaml`:
```yaml
sub_agent:
  profile: "default"  # profile for sub-agents, empty = current profile
  auto_fix: false     # if true, skip user confirmation before spawning fix agent
```

## Container Architecture

- Single container (`lainos`), systemd as PID 1
- **NEVER** use `docker stop $(docker ps -aq)` — stops everything
- Use `podman-compose` for lifecycle management
- User `lainos` created at build time with `/bin/bash` as login shell; `lain` is started via `/etc/motd` instructions or `shell.sh`
- Restart individual services inside the container via SSH:
  ```bash
  ssh -p 2222 lainos@localhost
  systemctl --user restart wh-gateway
  systemctl --user restart camofox
  ```
- Container uses `stop_signal: SIGRTMIN+3` for clean systemd shutdown

## Volumes

| Host mount | Container path | Purpose |
|---|---|---|
| `./.data/home/` | `/home/lainos/` | Entire home directory (config, workspace, dotfiles) |
| `./.data/snapshots/` | `/snapshots` | Btrfs snapshot storage |
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

## Camofox

Camofox is installed at `/home/lainos/camofox-browser/` (cloned from `jo-inc/camofox-browser` at build time). It runs as a `camofox.service` user-level systemd unit.

- **Install path**: `/home/lainos/camofox-browser/`
- **Port**: 9377
- **Node.js**: installed via nix (`nixpkgs#nodejs_20`)
- **Browser binary**: Camoufox, cached in `~/.cache/camoufox/`
- **Session data**: `~/.camofox/` (profiles, cookies, traces)
- **Telemetry**: disabled (`CAMOFOX_CRASH_REPORT_ENABLED=false`)

Key environment variables can be overridden via `systemctl --user edit camofox`. See `docs/camofox.md` for full API reference.

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

## Code Style

- No comments unless explicitly asked
- Follow existing Go conventions in the codebase
- Keep all gateway source in `gateway/` as a flat `main` package
- Keep all lain source in `lain/` as a flat `main` package
- Use `log/slog` for structured logging
- Use standard library HTTP types (`net/http`)
- Lua plugins: use `lain.*` API for TUI integration; full standard library available including `io`/`os` for filesystem and system access; use `lain.exec()` for command execution in render/callback contexts; keep render functions fast (called every frame)

## Testing

After modifying Go code, always verify:
```bash
cd gateway && go build ./...
cd lain && go build ./...
```

If test files exist:
```bash
cd gateway && go test ./...
cd lain && go test ./...
```

## Documentation

User-facing documentation lives in `docs/` and is available inside the container at `/home/lainos/docs/`. When working on tasks, consult the docs folder for information about the gateway, lain, container management, cloudflare setup, camofox, webhook handler examples, and configuration. Use grep/glob to search `docs/` for relevant keywords before asking the user for help.

## File conventions

- Build file is `Containerfile` (not `Dockerfile`) — podman naming
- Compose file is `compose.yaml` (not `docker-compose.yml`)
- Example configs use `.example.yaml` / `.example.md` suffix in `config/`
- Snapshot config uses `.conf` suffix in `config/` (sourced as shell variables)
- Systemd unit files go in `systemd/`
- Shell scripts go in `scripts/`
- Systemd presets go in `systemd/` as `98-lainos.preset` (must sort before `99-default-disable.preset`)
- Plugin files are `.lua` in `~/.config/lain/plugins/`
- Plugin state files are `.json` in `~/.config/lain/plugins/state/`

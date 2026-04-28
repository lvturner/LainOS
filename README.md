# LainOS

A self-contained container platform that pairs an HTTP webhook gateway with an LLM-powered chat CLI. Runs as a single privileged systemd container on [ucore-minimal](https://github.com/ublue-os/ucore) (Fedora CoreOS) with Cloudflare tunneling for ingress.

**Components:**

- **[wh-gateway](docs/wh-gateway.md)** — Go HTTP server that routes incoming webhooks to user-defined scripts
- **[lain](docs/lain.md)** — Interactive LLM chat CLI with MCP tool support, profile-based configuration, and a terminal UI
- **[cloudflared](docs/cloudflare-tunnel.md)** — Cloudflare tunnel client for external traffic ingress

## Quick Start

```bash
# Build and start (prompts for lain profile on first run)
./start.sh
```

`start.sh` handles everything: copying example configs, setting up the lain profile, building the container, and displaying credentials. It drops you into a lain shell when done.

Manual alternative:

```bash
# 1. Copy and edit configs
cp config/gateway.example.yaml config/gateway.yaml
cp config/cloudflared.example.yaml config/cloudflared.yaml

# 2. Build and run
podman-compose up -d

# 3. Get the auto-generated SSH password
podman logs lainos | head -10

# 4. SSH in (drops into lain TUI)
ssh -p 2222 lainos@localhost
```

## Project Structure

```
config/           → gateway.yaml, cloudflared.yaml, lain profiles (bind-mounted)
workspace/        → Handler scripts (bind-mounted)
local/            → ~/.local for the lainos user (bind-mounted)
docs/             → Documentation (COPY'd into container at /home/lainos/docs/)
gateway/          → Go webhook gateway source
lain/             → Go lain CLI source
systemd/          → Container systemd unit files
scripts/          → First-boot setup, init wrapper
Containerfile     → Multi-stage container build
compose.yaml      → podman-compose definition
```

## Webhook Gateway

Routes incoming HTTP requests to executable scripts. Request body is piped to stdin, headers are available as `WH_HEADER_*` environment variables, and the script's stdout/stderr maps directly to HTTP responses. Config changes to `gateway.yaml` are hot-reloaded without restart.

See **[docs/wh-gateway.md](docs/wh-gateway.md)** for route configuration, handler script examples, environment variables, and response behavior.

## Lain CLI

The default login shell for the `lainos` user. Supports interactive TUI mode and one-shot mode (`--prompt`). Each profile configures an LLM connection, MCP servers, and system instructions. Built-in tools include `run_command` for shell execution and `extend_timeout` for long-running operations.

See **[docs/lain.md](docs/lain.md)** for profiles, configuration, MCP servers, context compaction, and usage.

## Cloudflare Tunnel

cloudflared is installed in the container and managed by systemd. It tunnels external traffic to the webhook gateway on `localhost:8080`.

See **[docs/cloudflare-tunnel.md](docs/cloudflare-tunnel.md)** for tunnel creation, DNS routing, and ingress configuration.

## SSH Access

The container generates a random password on first boot:

```bash
podman logs lainos | head -10
```

```bash
ssh -p 2222 lainos@localhost
```

The `lainos` user's login shell is `/usr/local/bin/lain` — SSH sessions open directly into the lain TUI.

## Package Management

The container has [nix](https://nixos.org) installed for the `lainos` user. Use it to install tools that handler scripts or lain need:

```bash
nix profile install nixpkgs#jq
nix profile install nixpkgs#curl
nix profile list
nix profile remove 0
nix-collect-garbage
```

Installed binaries are immediately available in PATH. The container's root filesystem is immutable (ostree), so nix is the recommended way to add software.

## Container Management

```bash
./start.sh                      # Build, start, open lain shell
podman-compose up -d --build    # Rebuild after code changes
podman logs -f lainos           # View logs
podman-compose down             # Stop
```

See **[docs/container-management.md](docs/container-management.md)** for systemd service management, volume mounts, and container lifecycle.

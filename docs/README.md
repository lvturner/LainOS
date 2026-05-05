# lainos Documentation

Documentation for the lainos webhook gateway platform. These docs are available inside the container at `/home/lainos/docs/`.

## Contents

- **[wh-gateway.md](wh-gateway.md)** — Configuring and using the webhook HTTP gateway: routes, handler scripts, environment variables, response behavior, hot-reload, and installing tools
- **[lain.md](lain.md)** — The lain LLM chat CLI: profiles, config fields, MCP servers, built-in tools, context compaction, MDI window management, Lua plugin system, and nix package management
- **[camofox.md](camofox.md)** — Anti-detection headless browser server: API reference, session persistence, search macros, service management
- **[cloudflare-tunnel.md](cloudflare-tunnel.md)** — Setting up Cloudflare tunnels: creating new tunnels, using existing ones, DNS routing, and ingress configuration
- **[container-management.md](container-management.md)** — Container lifecycle: build, start, stop, SSH access, systemd services, and volume mounts
- **[snapshots.md](snapshots.md)** — Home directory snapshots: setup, configuration, restore, retention policy, and troubleshooting
- **[examples.md](examples.md)** — Webhook handler examples: GitHub signature verification, push/deploy/issue/PR/release handlers, and the async handler pattern

## Quick Start

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
gateway/          → Go gateway source
lain/             → Go lain CLI source
systemd/          → Container systemd unit files
scripts/          → First-boot setup, init wrapper
docs/             → This documentation (COPY'd into container)
Containerfile     → Multi-stage container build
compose.yaml      → podman-compose definition
```

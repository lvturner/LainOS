# LainOS

An AI-native container operating system — a single container that gives an AI agent a real Linux environment with systemd services, webhook routing, and nix package management. Works out of the box with `./start.sh` and minimal config.

Agent frameworks like [Hermes](https://github.com/nousresearch/hermes-agent) and [OpenClaw](https://github.com/openclaw/openclaw) layer AI tooling on top of an existing system. LainOS takes the opposite approach: it provides a purpose-built, but isolated, environment where the AI agent has full control over a real OS. The built-in webhook gateway lets you create deterministic workflows — scripts that respond to triggers without an LLM in the loop, so repetitive tasks don't burn tokens.

**What's included:**

- **[lain](docs/lain.md)** — Interactive LLM CLI with MCP tool support (the agent's interface to the system)
- **[wh-gateway](docs/wh-gateway.md)** — HTTP webhook gateway for deterministic workflows (no LLM required)
- **[cloudflared](docs/cloudflare-tunnel.md)** — Cloudflare tunnel for external ingress

## Quick Start

```bash
# Build and start (prompts for lain profile on first run)
./start.sh
```

`start.sh` handles everything: copying example configs, setting up the lain profile, building the container, and displaying credentials. It drops you into a lain shell when done. When you exit the shell, the container keeps running in the background. Re-attach with `./shell.sh`.

Manual alternative:

```bash
# 1. Copy and edit configs
cp config/gateway.example.yaml config/gateway.yaml
cp config/cloudflared.example.yaml config/cloudflared.yaml

# 2. Build and run
podman-compose up -d

# 3. Get the auto-generated SSH password
podman logs lainos | head -10

# 4. SSH in (bash shell; type 'lain' to start the AI shell)
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

The `lainos` user's login shell is `bash`. The `lain` command starts the interactive LLM TUI with MCP tool support. Supports interactive TUI mode and one-shot mode (`--prompt`). Each profile configures an LLM connection, MCP servers, and system instructions. Built-in tools include `run_command` for shell execution and `extend_timeout` for long-running operations.

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

The `lainos` user's login shell is `bash`. After logging in, you'll see instructions for starting `lain`.

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
./shell.sh                      # Re-open lain shell (container runs in background)
podman-compose up -d --build    # Rebuild after code changes
podman logs -f lainos           # View logs
podman-compose down             # Stop
```

See **[docs/container-management.md](docs/container-management.md)** for systemd service management, volume mounts, and container lifecycle.

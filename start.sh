#!/bin/bash
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
DIM='\033[2m'
RESET='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

ensure_config() {
    local src="$1"
    local dest="$2"
    local name="$3"

    if [ ! -f "$dest" ]; then
        if [ -f "$src" ]; then
            cp "$src" "$dest"
            echo -e "  ${GREEN}created${RESET} $name from example"
        else
            echo -e "  ${RED}missing${RESET} $name — no example file found"
            return 1
        fi
    else
        echo -e "  ${DIM}exists${RESET}  $name"
    fi
}

echo ""
echo -e "${BOLD}  ── security check ──────────────────────────${RESET}"
echo ""

CURRENT_USER=$(whoami)

if [ "$CURRENT_USER" != "lainos" ]; then
    echo -e "  ${YELLOW}LainOS gives AI agents access to a real Linux environment.${RESET}"
    echo -e "  ${YELLOW}Running under a dedicated user isolates potential damage.${RESET}"
    echo ""
    echo -e "  ${DIM}To run as a dedicated user:${RESET}"
    echo ""
    echo -e "    ${CYAN}sudo useradd -m lainos${RESET}"
    echo -e "    ${CYAN}sudo cp -r $SCRIPT_DIR /home/lainos/wh-gateway${RESET}"
    echo -e "    ${CYAN}sudo chown -R lainos:lainos /home/lainos/wh-gateway${RESET}"
    echo -e "    ${CYAN}sudo su - lainos${RESET}"
    echo -e "    ${CYAN}cd ~/wh-gateway && ./start.sh${RESET}"
    echo ""
    echo -e "  ${RED}⚠  Continuing as ${CURRENT_USER} gives the agent full access to your account.${RESET}"
    echo -e "  ${RED}   Unintended data loss is possible. Continue? [y/N]${RESET}"
    echo ""
    read -p "  " confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

echo ""
echo -e "${CYAN}   |          _)        _ \   ___|  "
echo -e "${CYAN}   |      _\` | | __ \  |   |\___ \  "
echo -e "${CYAN}   |     (   | | |   | |   |      | "
echo -e "${CYAN}_____|\\__,_|_|_|  _|\\___/ _____/  "
echo -e "${CYAN}                                   ${RESET}"
echo ""
echo -e "  ${BOLD}LainOS${RESET} ${DIM}• ucore + podman${RESET}"
echo ""
echo -e "${BOLD}  ── checking config ──────────────────────────${RESET}"
echo ""

ensure_config config/gateway.example.yaml config/gateway.yaml "gateway.yaml"
ensure_config config/cloudflared.example.yaml config/cloudflared.yaml "cloudflared.yaml"

if [ ! -d workspace ]; then
    mkdir -p workspace
    echo -e "  ${GREEN}created${RESET} workspace/"
else
    echo -e "  ${DIM}exists${RESET}  workspace/"
fi

if [ ! -d local ]; then
    mkdir -p local
    echo -e "  ${GREEN}created${RESET} local/"
else
    echo -e "  ${DIM}exists${RESET}  local/"
fi

echo ""
echo -e "${BOLD}  ── lain profile setup ──────────────────────${RESET}"
echo ""

LAIN_DIR="config/lain/profiles/default"
mkdir -p "$LAIN_DIR"

if [ ! -f "$LAIN_DIR/config.yaml" ]; then
    echo -e "  ${CYAN}No lain default profile found.${RESET}"
    echo -e "  ${DIM}Press Enter for defaults${RESET}"
    echo ""
    read -p "  API URL [https://api.deepseek.com/v1]: " api_url
    api_url="${api_url:-https://api.deepseek.com/v1}"
    read -p "  API Key: " api_key
    if [ -z "$api_key" ]; then
        echo -e "  ${RED}API key is required.${RESET}"
    else
        read -p "  Model [deepseek-chat]: " model
        model="${model:-deepseek-chat}"
        read -p "  Temperature [0.7]: " temperature
        temperature="${temperature:-0.7}"
        cat > "$LAIN_DIR/config.yaml" <<YAML
api_url: "${api_url}"
api_key: "${api_key}"
model: "${model}"
temperature: ${temperature}
max_tokens: 4096
YAML
        echo -e "  ${GREEN}created${RESET} lain default profile config"
    fi
else
    echo -e "  ${DIM}exists${RESET}  lain default profile config"
fi

if [ ! -f "$LAIN_DIR/agents.md" ]; then
    cat > "$LAIN_DIR/agents.md" <<'AGENTS'
# Lain Assistant

You are a helpful coding and system administration assistant running inside a Linux container.

You have access to system commands via the `run_command` tool. Use it freely to:
- Explore the filesystem
- Run build and test commands
- Inspect running processes and services
- Execute any shell commands needed to help the user

Always explain what you're doing before running commands. When writing code or config files,
show the content first, then write it.

Be concise. Prefer action over explanation.

## No `sudo`

The `lainos` user is unprivileged. **Never use `sudo`.** It is not available and will fail.

If you need to install software, use `nix`:

- `nix profile install nixpkgs#<package>` to install a package
- Installed binaries are automatically available in PATH
- `nix profile list` to see installed packages
- `nix profile remove <index>` to remove a package
- `nix-collect-garbage` to free disk space from old packages

This keeps the host container clean while giving you full package access.

## Task Management

You have a `todo` tool for tracking tasks across a session. Use it to stay organized on multi-step work.

### When to use it

- When the user asks you to do something with multiple steps
- When you're working through a sequence of changes (files, configs, commands)
- When the user asks you to track progress on something

### How to use it

- `todo(action="add", task="description")` — add a task
- `todo(action="list")` — show all tasks and their status
- `todo(action="complete", id=N)` — mark task N as done
- `todo(action="uncomplete", id=N)` — revert task N to pending
- `todo(action="remove", id=N)` — delete a task
- `todo(action="clear")` — remove all completed tasks

### After context compaction

When the system compacts the conversation context, your task list is automatically injected into the new context. You should still call `todo(action="list")` to verify your progress and ensure nothing was lost. If the user's original goal involved tracked tasks, continue working through the remaining items.
AGENTS
    echo -e "  ${GREEN}created${RESET} lain default agents.md"
else
    echo -e "  ${DIM}exists${RESET}  lain agents.md"
fi

if [ ! -f "$LAIN_DIR/servers.json" ]; then
    echo '{"mcpServers":{}}' > "$LAIN_DIR/servers.json"
    echo -e "  ${GREEN}created${RESET} lain default servers.json"
else
    echo -e "  ${DIM}exists${RESET}  lain servers.json"
fi

echo ""
echo -e "${BOLD}  ── matching host UID ────────────────────────${RESET}"
echo ""

HOST_UID=$(id -u)
sed -i "s/-u [0-9]\+/-u ${HOST_UID}/" Containerfile
echo -e "  ${GREEN}set${RESET} lainos UID to ${HOST_UID}"

rm -f compose.override.yaml

echo ""
echo -e "${BOLD}  ── building & starting ──────────────────────${RESET}"
echo ""

podman-compose down 2>/dev/null || true
podman-compose up -d --build --force-recreate 2>&1 | while IFS= read -r line; do
    echo -e "  $line"
done

echo ""
echo -e "${BOLD}  ── waiting for container ────────────────────${RESET}"
echo ""

for i in $(seq 1 10); do
    if podman exec lainos systemctl is-system-running >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

if ! podman exec lainos test -f /var/lib/lainos/.password 2>/dev/null; then
    podman exec lainos systemctl start first-boot-setup.service 2>/dev/null || true
    sleep 2
fi

PASSWORD=$(podman exec lainos cat /var/lib/lainos/.password 2>/dev/null || true)

if [ -z "$PASSWORD" ]; then
    echo -e "  ${RED}Could not retrieve password.${RESET}"
    echo -e "  ${DIM}Try: podman exec lainos cat /var/lib/lainos/.password${RESET}"
    PASSWORD="(unavailable)"
fi

echo -e "${BOLD}  ── credentials ──────────────────────────────${RESET}"
echo ""
echo -e "  ${CYAN}SSH Access${RESET}"
echo -e "  ${DIM}──────────────────────────${RESET}"
echo -e "  Host:     ${GREEN}localhost${RESET}"
echo -e "  Port:     ${GREEN}2222${RESET}"
echo -e "  User:     ${GREEN}lainos${RESET}"
echo -e "  Password: ${YELLOW}${PASSWORD}${RESET}"
echo ""

echo ""
echo -e "  ${DIM}Logs:${RESET}  podman logs -f lainos"
echo -e "  ${DIM}Stop:${RESET}   podman-compose down"
echo ""
echo -e "${GREEN}  Opening LainOS shell...${RESET}"
echo ""

podman exec -it -u lainos -w /home/lainos lainos /usr/local/bin/lain

echo ""
echo -e "${GREEN}  LainOS is still running in the background.${RESET}"
echo ""
echo -e "  ${DIM}Shell:${RESET}   ./shell.sh"
echo -e "  ${DIM}Logs:${RESET}    podman logs -f lainos"
echo -e "  ${DIM}Stop:${RESET}    podman-compose down"
echo ""

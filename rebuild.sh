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

echo ""
echo -e "${CYAN}   |          _)        _ \   ___|  "
echo -e "${CYAN}   |      _\` | | __ \  |   |\___ \  "
echo -e "${CYAN}   |     (   | | |   | |   |      | "
echo -e "${CYAN}_____|\\__,_|_|_|  _|\\___/ _____/  "
echo -e "${CYAN}                                   ${RESET}"
echo ""
echo -e "  ${BOLD}Rebuild${RESET} ${DIM}• pull latest base, rebuild, restart${RESET}"
echo ""
echo -e "  ${DIM}This will briefly stop the container. All volumes and data${RESET}"
echo -e "  ${DIM}in .data/ and the nix store are preserved.${RESET}"
echo ""
echo -ne "  ${YELLOW}Continue? [y/N]${RESET} "
read -r confirm
if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
    echo -e "  ${DIM}Cancelled.${RESET}"
    exit 0
fi

echo ""
echo -e "${BOLD}  ── pulling latest base image ───────────────${RESET}"
echo ""

podman pull ghcr.io/ublue-os/ucore-minimal:stable 2>&1 | while IFS= read -r line; do
    echo -e "  $line"
done

echo ""
echo -e "${BOLD}  ── stopping container ──────────────────────${RESET}"
echo ""

podman-compose down 2>&1 | while IFS= read -r line; do
    echo -e "  $line"
done

echo ""
echo -e "${BOLD}  ── rebuilding image ────────────────────────${RESET}"
echo ""

podman-compose build 2>&1 | while IFS= read -r line; do
    echo -e "  $line"
done

echo ""
echo -e "${BOLD}  ── starting container ──────────────────────${RESET}"
echo ""

podman-compose up -d --force-recreate 2>&1 | while IFS= read -r line; do
    echo -e "  $line"
done

echo ""
echo -e "${BOLD}  ── waiting for container ────────────────────${RESET}"
echo ""

for i in $(seq 1 30); do
    STATE=$(podman exec lainos systemctl is-system-running 2>/dev/null || true)
    if [[ "$STATE" == "running" || "$STATE" == "degraded" ]]; then
        break
    fi
    sleep 1
done

if [[ "$STATE" != "running" && "$STATE" != "degraded" ]]; then
    echo -e "  ${RED}Container did not start within 30s (state: ${STATE})${RESET}"
    echo -e "  ${DIM}Check logs: podman logs lainos${RESET}"
    exit 1
fi

echo -e "  ${GREEN}Container is ${STATE}.${RESET}"

echo ""
echo -e "${BOLD}  ── credentials ──────────────────────────────${RESET}"
echo ""

PASSWORD=$(podman exec lainos cat /var/lib/lainos/.password 2>/dev/null || true)

if [ -z "$PASSWORD" ]; then
    echo -e "  ${DIM}(password file not found — may have been cleared)${RESET}"
else
    echo -e "  ${CYAN}SSH Access${RESET}"
    echo -e "  ${DIM}──────────────────────────${RESET}"
    echo -e "  Host:     ${GREEN}localhost${RESET}"
    echo -e "  Port:     ${GREEN}2222${RESET}"
    echo -e "  User:     ${GREEN}lainos${RESET}"
    echo -e "  Password: ${YELLOW}${PASSWORD}${RESET}"
fi

echo ""
echo -e "  ${DIM}Shell:${RESET}   ./shell.sh"
echo -e "  ${DIM}Logs:${RESET}    podman logs -f lainos"
echo -e "  ${DIM}Stop:${RESET}    podman-compose down"
echo ""
echo -e "${GREEN}  Rebuild complete.${RESET}"
echo ""

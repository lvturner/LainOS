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

source "$SCRIPT_DIR/scripts/common.sh"

CONTAINER="lainos"

BINARY_MAP=(
    "lain:/usr/sbin/lain"
    "gateway:/usr/sbin/wh-gateway"
)

build_and_deploy() {
    local src_dir="$1"
    local dest="$2"

    if [ ! -d "$src_dir" ]; then
        echo -e "  ${RED}skip${RESET} ${src_dir}/ not found"
        return 1
    fi

    echo -e "  ${CYAN}build${RESET} ${src_dir}/ → ${dest}"

    CGO_ENABLED=0 go build -C "$src_dir" -o /tmp/lainos-deploy-binary .

    echo -e "  ${CYAN}copy${RESET}  → ${CONTAINER}:${dest}"
    $RUNTIME cp /tmp/lainos-deploy-binary "${CONTAINER}:${dest}"
    rm -f /tmp/lainos-deploy-binary

    echo -e "  ${GREEN}done${RESET}  ${src_dir}/ deployed"
}

if ! detect_runtime; then
    exit 1
fi

if ! $RUNTIME ps --filter "name=${CONTAINER}" --format '{{.Names}}' 2>/dev/null | grep -q "^${CONTAINER}$"; then
    echo -e "  ${RED}Container '${CONTAINER}' is not running.${RESET}"
    echo -e "  ${DIM}Start it first: ${COMPOSE_CMD} up -d${RESET}"
    exit 1
fi

TARGET="${1:-all}"
DEPLOYED=0

echo ""
echo -e "${BOLD}  ── deploy binary ──────────────────────────${RESET}"
echo ""

for entry in "${BINARY_MAP[@]}"; do
    src_dir="${entry%%:*}"
    dest="${entry##*:}"

    if [[ "$TARGET" == "all" || "$TARGET" == "$src_dir" ]]; then
        build_and_deploy "$src_dir" "$dest" && ((DEPLOYED++)) || true
    fi
done

if [[ "$DEPLOYED" -eq 0 ]]; then
    echo -e "  ${YELLOW}Nothing deployed.${RESET}"
    echo -e "  ${DIM}Usage: $0 [all|lain|gateway]${RESET}"
fi

echo ""

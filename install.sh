#!/bin/bash
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
DIM='\033[2m'
RESET='\033[0m'

REPO_URL="https://github.com/lvturner/LainOS.git"

detect_os() {
    if [[ "$(uname -s)" == "Darwin" ]]; then
        OS_ID="macos"
        return
    fi

    if [[ -f /etc/os-release ]]; then
        source /etc/os-release
        case "$ID" in
            fedora|rhel|centos|rocky|alma|nobara)
                OS_ID="fedora"
                return
                ;;
            ubuntu|debian|linuxmint|pop|elementary|kali)
                OS_ID="ubuntu"
                return
                ;;
        esac
        for like in $ID_LIKE; do
            case "$like" in
                fedora|rhel|centos)
                    OS_ID="fedora"
                    return
                    ;;
                debian|ubuntu)
                    OS_ID="ubuntu"
                    return
                    ;;
            esac
        done
    fi

    OS_ID="unknown"
}

check_git() {
    if command -v git &>/dev/null; then
        return 0
    fi

    detect_os

    echo ""
    echo -e "  ${RED}git is not installed.${RESET}"
    echo -e "  ${YELLOW}Install it to continue:${RESET}"
    echo ""

    case "$OS_ID" in
        macos)
            echo -e "  ${CYAN}xcode-select --install${RESET}"
            echo -e "  ${DIM}or: brew install git${RESET}"
            ;;
        fedora)
            echo -e "  ${CYAN}sudo dnf install git${RESET}"
            ;;
        ubuntu)
            echo -e "  ${CYAN}sudo apt install git${RESET}"
            ;;
        *)
            echo -e "  ${CYAN}Install git for your system.${RESET}"
            ;;
    esac

    echo ""
    return 1
}

offer_install() {
    local desc="$1"
    shift
    local cmd="$*"

    echo -e "  ${YELLOW}${desc} is not installed.${RESET}"
    echo -e "  ${CYAN}${cmd}${RESET}"
    echo ""
    read -p "  Install now? [Y/n] " confirm
    if [[ "$confirm" =~ ^[Nn]$ ]]; then
        return 1
    fi
    echo ""
    echo -e "  ${DIM}Running: ${cmd}${RESET}"
    echo ""
    eval "$cmd"
}

check_runtime() {
    if command -v podman &>/dev/null && command -v podman-compose &>/dev/null; then
        RUNTIME="podman"
        COMPOSE_CMD="podman-compose"
        return 0
    fi

    if command -v docker &>/dev/null && docker compose version &>/dev/null 2>&1; then
        RUNTIME="docker"
        COMPOSE_CMD="docker compose"
        return 0
    fi

    detect_os

    echo ""
    echo -e "  ${YELLOW}No container runtime found.${RESET}"
    echo ""

    local podman_cmd docker_cmd

    case "$OS_ID" in
        macos)
            podman_cmd="brew install podman podman-compose"
            docker_cmd="brew install --cask docker"
            ;;
        fedora)
            podman_cmd="sudo dnf install -y podman podman-compose"
            docker_cmd="sudo dnf install -y docker docker-compose-plugin"
            ;;
        ubuntu)
            podman_cmd="sudo apt install -y podman podman-compose"
            docker_cmd="sudo apt install -y docker.io docker-compose-plugin"
            ;;
        *)
            echo -e "  ${RED}Cannot auto-install on this system.${RESET}"
            echo -e "  Install podman or docker manually:"
            echo -e "  ${DIM}https://podman.io/getting-started/installation${RESET}"
            echo -e "  ${DIM}https://docs.docker.com/get-docker/${RESET}"
            echo ""
            return 1
            ;;
    esac

    echo -e "  ${BOLD}Option 1: podman (recommended)${RESET}"
    if offer_install "podman + podman-compose" "$podman_cmd"; then
        if [[ "$OS_ID" == "macos" ]]; then
            echo ""
            echo -e "  ${CYAN}Initializing podman machine...${RESET}"
            podman machine init || true
        fi
        RUNTIME="podman"
        COMPOSE_CMD="podman-compose"
        return 0
    fi

    echo ""
    echo -e "  ${BOLD}Option 2: docker (fallback)${RESET}"
    if offer_install "docker + compose plugin" "$docker_cmd"; then
        RUNTIME="docker"
        COMPOSE_CMD="docker compose"
        return 0
    fi

    echo ""
    echo -e "  ${RED}A container runtime is required. Exiting.${RESET}"
    echo ""
    return 1
}

echo ""
echo -e "${CYAN}   |          _)        _ \   ___|  "
echo -e "${CYAN}   |      _\` | | __ \  |   |\___ \  "
echo -e "${CYAN}   |     (   | | |   | |   |      | "
echo -e "${CYAN}_____|\\__,_|_|_|  _|\\___/ _____/  "
echo -e "${CYAN}                                   ${RESET}"
echo ""
echo -e "  ${BOLD}LainOS Installer${RESET}"
echo ""

echo -e "${BOLD}  ── checking dependencies ──────────────────${RESET}"
echo ""

if ! check_git; then
    exit 1
fi
echo -e "  ${GREEN}git${RESET} found"

if ! check_runtime; then
    exit 1
fi
echo -e "  ${GREEN}${RUNTIME}${RESET} + ${GREEN}compose${RESET} found"

echo ""
echo -e "${BOLD}  ── choosing install location ──────────────${RESET}"
echo ""

read -p "  Install path [~/LainOS]: " install_path
install_path="${install_path:-$HOME/LainOS}"
install_path="$(eval echo "$install_path")"

if [[ -d "$install_path" ]]; then
    if [[ -d "$install_path/.git" ]]; then
        echo -e "  ${YELLOW}Existing LainOS repo found at ${install_path}${RESET}"
        echo -e "  ${DIM}Pulling latest changes...${RESET}"
        git -C "$install_path" pull --ff-only || {
            echo -e "  ${RED}Could not pull. Fix conflicts or choose a different path.${RESET}"
            exit 1
        }
        echo -e "  ${GREEN}updated${RESET} $install_path"
    else
        echo -e "  ${RED}${install_path} exists but is not a LainOS repo.${RESET}"
        echo -e "  ${RED}Choose a different path.${RESET}"
        exit 1
    fi
else
    echo -e "  ${DIM}Cloning into ${install_path}...${RESET}"
    git clone "$REPO_URL" "$install_path"
    echo -e "  ${GREEN}cloned${RESET} to $install_path"
fi

echo ""
echo -e "${GREEN}  Starting LainOS...${RESET}"
echo ""

cd "$install_path"
exec ./start.sh

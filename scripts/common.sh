#!/bin/bash

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

_detect_runtime() {
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

    return 1
}

detect_runtime() {
    if _detect_runtime; then
        return 0
    fi

    detect_os

    echo ""
    echo -e "  ${RED}No container runtime found.${RESET}"
    echo -e "  ${YELLOW}Install podman (recommended) or docker to continue.${RESET}"
    echo ""

    case "$OS_ID" in
        macos)
            echo -e "  ${CYAN}macOS:${RESET}"
            echo -e "    brew install podman podman-compose"
            echo -e "    podman machine init"
            echo ""
            echo -e "  ${DIM}Or install Docker Desktop:${RESET}"
            echo -e "    brew install --cask docker"
            ;;
        fedora)
            echo -e "  ${CYAN}Fedora/RHEL:${RESET}"
            echo -e "    sudo dnf install podman podman-compose"
            echo ""
            echo -e "  ${DIM}Or install docker:${RESET}"
            echo -e "    sudo dnf install docker docker-compose-plugin"
            ;;
        ubuntu)
            echo -e "  ${CYAN}Ubuntu/Debian:${RESET}"
            echo -e "    sudo apt install podman podman-compose"
            echo ""
            echo -e "  ${DIM}Or install docker:${RESET}"
            echo -e "    sudo apt install docker.io docker-compose-plugin"
            ;;
        *)
            echo -e "  ${CYAN}Install podman or docker for your system.${RESET}"
            echo -e "  ${DIM}https://podman.io/getting-started/installation${RESET}"
            echo -e "  ${DIM}https://docs.docker.com/get-docker/${RESET}"
            ;;
    esac

    echo ""
    return 1
}

print_runtime() {
    if [[ "$RUNTIME" == "podman" ]]; then
        echo -e "  ${GREEN}podman${RESET} + ${GREEN}podman-compose${RESET}"
    else
        echo -e "  ${YELLOW}docker${RESET} + ${YELLOW}docker compose${RESET} ${DIM}(podman not found, using docker fallback)${RESET}"
    fi
}

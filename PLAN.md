# lainos — Webhook HTTP Gateway

## Overview

An HTTP gateway that processes incoming webhooks, matches them to configured routes, and executes commands with the webhook payload. Built on ucore-minimal (Fedora CoreOS) as a single privileged system container managed by systemd.

## Architecture

### Container

- **Base image**: `ghcr.io/ublue-os/ucore-minimal:stable`
- **Runtime**: podman with `--privileged` and `--systemd=always`
- **PID 1**: systemd (native to ucore)
- **Container name**: `lainos`

### Processes managed by systemd

| Service | Description | Source |
|---|---|---|
| `sshd` | SSH access for the `lainos` user | Pre-installed in ucore |
| `wh-gateway` | Go HTTP webhook gateway | Our binary |
| `cloudflared` | Cloudflare tunnel client | Third-party binary |
| `first-boot-setup` | One-shot: generates random SSH password for `lainos` user | Our script |

### What ucore-minimal already provides

- systemd
- SSH daemon (password auth enabled)
- podman + docker (moby-engine)
- firewalld
- tmux, tailscale, wireguard-tools
- `core` user

We layer on top: the gateway binary, lain binary, cloudflared, distrobox, a `lainos` user, and systemd unit files.

---

## Project Structure

```
lainos/
├── Containerfile                    # Multi-stage build (podman naming convention)
├── compose.yaml                     # podman-compose compatible
├── gateway/
│   ├── go.mod
│   ├── main.go                      # Entry point, wires everything together
│   ├── config.go                    # YAML config structs + loader
│   ├── watcher.go                   # fsnotify hot-reload watcher
│   ├── server.go                    # HTTP server + dynamic routing
│   └── handler.go                   # Command execution (stdin + env vars)
├── lain/                            # Lain binary source
├── systemd/                         # systemd unit files
│   ├── wh-gateway.service
│   ├── cloudflared.service
│   └── first-boot-setup.service
├── scripts/
│   ├── first-boot-setup.sh          # Generates random password for lainos user
│   └── init-wrapper.sh              # Init entrypoint wrapper
├── docs/                            # Documentation (COPY'd into image)
├── config/                          # Example configs (user copies and edits these)
│   ├── gateway.example.yaml
│   └── cloudflared.example.yaml
└── workspace/                       # Scripts workspace (user bind-mounts this)
    └── examples/
        ├── lib/
        │   └── verify-github-signature.sh
        ├── handle-github.sh.example
        ├── handle-github-push.sh.example
        ├── handle-github-push-async.sh.example
        ├── handle-github-issues.sh.example
        ├── handle-github-issues-async.sh.example
        ├── handle-github-pr.sh.example
        ├── handle-github-pr-async.sh.example
        ├── handle-github-release.sh.example
        └── handle-github-release-async.sh.example
```

---

## Containerfile

Multi-stage build:

### Stage 1 — Build Go binary

```
FROM golang:1.23 AS builder
WORKDIR /build
COPY gateway/ .
RUN CGO_ENABLED=0 go build -o wh-gateway .
```

### Stage 2 — Build lain binary

```
FROM golang:1.25 AS lain-builder
WORKDIR /build
COPY lain/ .
RUN CGO_ENABLED=0 go build -o lain .
```

### Stage 3 — Runtime image

```
FROM ghcr.io/ublue-os/ucore-minimal:stable

RUN mkdir -p /var/usrlocal/bin && \
    rpm-ostree install distrobox https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-x86_64.rpm

COPY --from=builder /build/wh-gateway /usr/local/bin/wh-gateway
COPY --from=lain-builder /build/lain /usr/local/bin/lain

RUN useradd -m -s /usr/local/bin/lain lainos && \
    mkdir -p /home/lainos/config /home/lainos/workspace /home/lainos/workspace/logs && \
    chown -R lainos:lainos /home/lainos/config /home/lainos/workspace

COPY docs/ /home/lainos/docs/

COPY systemd/*.service /usr/lib/systemd/system/
COPY systemd/98-lainos.preset /usr/lib/systemd/system-preset/
COPY scripts/first-boot-setup.sh /usr/local/bin/first-boot-setup.sh
COPY scripts/init-wrapper.sh /usr/local/bin/init-wrapper.sh
RUN chmod +x /usr/local/bin/first-boot-setup.sh /usr/local/bin/init-wrapper.sh

VOLUME ["/home/lainos/config", "/home/lainos/workspace"]
CMD ["/usr/local/bin/init-wrapper.sh"]
```

Note: The `lainos` user is created at build time with `/usr/local/bin/lain` as login shell. Services are enabled via `98-lainos.preset` which must sort before `99-default-disable.preset`.

---

## compose.yaml

```yaml
services:
  lainos:
    build: .
    container_name: lainos
    privileged: true
    command: /usr/local/bin/init-wrapper.sh
    stop_signal: SIGRTMIN+3
    volumes:
      - ./config:/home/lainos/config:Z
      - ./workspace:/home/lainos/workspace:Z
      - ./config/lain:/home/lainos/.config/lain:Z
    ports:
      - "2222:22"
    restart: unless-stopped
```

Notes:
- `privileged: true` — required for systemd in a container
- `stop_signal: SIGRTMIN+3` — tells systemd to shut down cleanly
- `:Z` on volumes — SELinux relabel for podman

---

## Volumes

| Host Path | Container Path | Purpose |
|---|---|---|
| `./config/` | `/home/lainos/config/` | `gateway.yaml`, `cloudflared.yaml`, cloudflared credentials |
| `./workspace/` | `/home/lainos/workspace/` | Scripts, tools, anything the webhook commands need to run |
| `./config/lain/` | `/home/lainos/.config/lain/` | Lain profile config |

Users edit `./config/gateway.yaml` on the host; the gateway hot-reloads automatically. Scripts live in `./workspace/` and are referenced from the config.

---

## Systemd Services

### wh-gateway.service

```ini
[Unit]
Description=Webhook Gateway
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/wh-gateway -config /home/lainos/config/gateway.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### cloudflared.service

```ini
[Unit]
Description=Cloudflared Tunnel
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cloudflared tunnel --config /home/lainos/config/cloudflared.yaml run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### first-boot-setup.service

One-shot service that runs before sshd on first boot only.

```ini
[Unit]
Description=First Boot Setup
Before=sshd.service
ConditionPathExists=!/var/lib/lainos/.setup-complete

[Service]
Type=oneshot
ExecStart=/usr/local/bin/first-boot-setup.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
```

### scripts/first-boot-setup.sh

```bash
#!/bin/bash
set -e

PASSWORD=$(openssl rand -base64 18)
echo "lainos:$PASSWORD" | chpasswd

mkdir -p /var/lib/lainos
echo "$PASSWORD" > /var/lib/lainos/.password
chmod 600 /var/lib/lainos/.password
touch /var/lib/lainos/.setup-complete

echo "============================================"
echo " lainos SSH credentials:"
echo "   User:     lainos"
echo "   Password: $PASSWORD"
echo "   Port:     22"
echo "============================================"
```

---

## Gateway Configuration

### config/gateway.example.yaml

```yaml
server:
  listen: "0.0.0.0:8080"
  timeout: 30s

routes:
  - path: "/webhook/github"
    command: "/home/lainos/workspace/scripts/handle-github.sh"
    method: "POST"

  - path: "/webhook/deploy"
    command: "/home/lainos/workspace/scripts/deploy.sh"
    method: "POST"
```

### config/cloudflared.example.yaml

```yaml
tunnel: <your-tunnel-id>
credentials-file: /home/lainos/config/cloudflared/credentials.json
ingress:
  - hostname: webhooks.example.com
    service: http://localhost:8080
  - service: http_status:404
```

---

## Go Application

### Dependencies

- `gopkg.in/yaml.v3` — YAML parsing
- `github.com/fsnotify/fsnotify` — config file hot-reload

### Module: `main.go`

Entry point. Parses flags (`-config`), loads initial config, starts the fsnotify watcher, starts the HTTP server. Wires config reload events to the server's route table.

### Module: `config.go`

Defines the YAML config structs:

```go
type Config struct {
    Server ServerConfig `yaml:"server"`
    Routes []Route      `yaml:"routes"`
}

type ServerConfig struct {
    Listen string        `yaml:"listen"`
    Timeout time.Duration `yaml:"timeout"`
}

type Route struct {
    Path    string `yaml:"path"`
    Command string `yaml:"command"`
    Method  string `yaml:"method"`
}
```

Provides `LoadConfig(path string) (*Config, error)` that reads and parses the YAML file.

### Module: `watcher.go`

Uses `fsnotify` to watch the config file. On `Write` or `Create` events (debounced), reloads the config and sends the new config to the server via a channel. Debounce window of ~500ms to avoid reloads during partial writes.

### Module: `server.go`

HTTP server that:
1. Accepts a channel for config updates
2. Builds an `http.ServeMux` from the current config
3. Each route runs the command handler
4. On config change: atomically swaps the serve mux (no dropped connections)
5. Listens on the configured address

### Module: `handler.go`

Command execution handler that:
1. Validates the HTTP method matches the route config
2. Reads the request body
3. Sets environment variables:
   - `WH_METHOD` — HTTP method
   - `WH_PATH` — request path
   - `WH_QUERY` — query string
   - `WH_HEADER_*` — all HTTP headers (uppercased, dashes to underscores)
4. Pipes the request body to the command's stdin
5. Captures stdout/stderr
6. Returns the command's exit code as HTTP status:
   - Exit 0 → 200 OK with stdout as body
   - Exit non-zero → 500 with stderr as body
7. Enforces the configured timeout (kills the command if exceeded)

---

## Data Flow

```
External Request
    │
    ▼
Cloudflared Tunnel ──► localhost:8080
    │
    ▼
wh-gateway HTTP Server
    │
    ├── Route: /webhook/github
    │       │
    │       ▼
    │   Command Handler
    │       │
    │       ├─ Set env vars (WH_METHOD, WH_PATH, WH_QUERY, WH_HEADER_*)
    │       ├─ Pipe body to stdin
    │       ├─ Execute: /home/lainos/workspace/scripts/handle-github.sh
    │       └─ Return stdout as HTTP response
    │
    └── Route: /webhook/deploy
            │
            ▼
        Command Handler
            │
            ├─ Set env vars
            ├─ Pipe body to stdin
            ├─ Execute: /home/lainos/workspace/scripts/deploy.sh
            └─ Return stdout as HTTP response
```

---

## SSH Access

- **User**: `lainos` (created during image build)
- **Shell**: `/usr/local/bin/lain`
- **Password**: Random 24-character string generated on first boot
- **How to get the password**: `podman logs lainos` (printed to stdout)
- **How to connect**: `ssh -p 2222 lainos@localhost`

### Installing software

Since ucore has an immutable rootfs (ostree), use distrobox:

```bash
# Create a mutable container for tools
distrobox-enter --name dev --image fedora:latest

# Inside distrobox, install whatever you need
sudo dnf install htop nano curl jq

# Tools are available in the distrobox session
```

---

## Usage Flow

```bash
# 1. Run start.sh — builds and starts container
./start.sh

# 2. start.sh handles:
#    - Copying example configs if needed
#    - Setting up lain profile
#    - Building the container
#    - Displaying SSH credentials
#    - Opening a lain shell in the container

# Manual operations:
# Get the SSH password
podman logs lainos | head -10

# SSH in if needed
ssh -p 2222 lainos@localhost

# Stop
podman-compose down
```

---

## Hot Reload Behavior

1. Gateway watches `/home/lainos/config/gateway.yaml` via fsnotify
2. On file write/create events (debounced 500ms):
   - Reload and parse the new YAML
   - Build a new `http.ServeMux` from the updated routes
   - Atomically swap the server's handler
   - In-flight requests continue on the old route table
   - New requests use the updated routes
3. Log the reload event (routes added/removed/modified)

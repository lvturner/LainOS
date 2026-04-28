# wh-gateway

An HTTP gateway that receives webhooks and executes scripts. Runs as a privileged systemd container on [ucore-minimal](https://github.com/ublue-os/ucore) (Fedora CoreOS) with Cloudflare tunneling for ingress.

## Quick Start

```bash
# 1. Copy and edit configs
cp config/gateway.example.yaml config/gateway.yaml
cp config/cloudflared.example.yaml config/cloudflared.yaml

# 2. Build and run
podman-compose up -d

# 3. Get the auto-generated SSH password
podman logs lainos | head -10

# 4. SSH in if needed
ssh -p 2222 lainos@localhost
```

## Configuration

### gateway.yaml

This is the main config file. Changes are **hot-reloaded automatically** — no restart needed.

```yaml
server:
  listen: "0.0.0.0:8080"   # Address the gateway listens on inside the container
  timeout: 30s              # Max time a handler script can run

routes:
  - path: "/webhook/github"
    command: "/home/lainos/workspace/scripts/handle-github.sh"
    method: "POST"

  - path: "/webhook/deploy"
    command: "/home/lainos/workspace/scripts/deploy.sh"
    method: "POST"
```

| Field | Description | Default |
|---|---|---|
| `server.listen` | Listen address | `0.0.0.0:8080` |
| `server.timeout` | Command execution timeout | `30s` |
| `routes[].path` | URL path to match | required |
| `routes[].command` | Script to execute | required |
| `routes[].method` | HTTP method to accept | `POST` |

### Hot Reload

The gateway watches `gateway.yaml` via fsnotify. On file write/create events (debounced 500ms):

1. Reload and parse the new YAML
2. Build a new `http.ServeMux` from the updated routes
3. Atomically swap the server's handler
4. In-flight requests continue on the old route table; new requests use the updated routes

## Registering a New Webhook

### 1. Write a handler script

Place it in `workspace/scripts/` and make it executable:

```bash
mkdir -p workspace/scripts
cat > workspace/scripts/handle-deploy.sh << 'EOF'
#!/bin/bash
set -e

# The webhook body is available on stdin
BODY=$(cat)

# These env vars are always set:
#   WH_METHOD  - HTTP method (e.g. POST)
#   WH_PATH    - Request path (e.g. /webhook/deploy)
#   WH_QUERY   - Raw query string
#   WH_HEADER_<NAME> - Each HTTP header (dashes → underscores, uppercased)

echo "Deploy triggered"
echo "Body: $BODY"

# Do something useful here
# git pull && make deploy
EOF
chmod +x workspace/scripts/handle-deploy.sh
```

### 2. Add a route to config/gateway.yaml

```yaml
routes:
  # ...existing routes...

  - path: "/webhook/deploy"
    command: "/home/lainos/workspace/scripts/handle-deploy.sh"
    method: "POST"
```

The config is **hot-reloaded** — save the file and the new route is live immediately.

### 3. Update Cloudflare ingress (if using a new hostname)

If the new hook needs its own domain, add an ingress rule to `config/cloudflared.yaml` and restart cloudflared inside the container:

```bash
ssh -p 2222 lainos@localhost
sudo systemctl restart cloudflared
```

If you're using a single hostname with path-based routing, no Cloudflare changes are needed.

## Environment Variables Available in Scripts

Every handler script receives these environment variables:

| Variable | Example | Description |
|---|---|---|
| `WH_METHOD` | `POST` | HTTP method of the request |
| `WH_PATH` | `/webhook/github` | URL path that matched |
| `WH_QUERY` | `ref=main` | Raw query string |
| `WH_HEADER_X_GITHUB_EVENT` | `push` | The `X-GitHub-Event` header |
| `WH_HEADER_CONTENT_TYPE` | `application/json` | The `Content-Type` header |

Any HTTP header is available as `WH_HEADER_<UPPERCASED>` with dashes converted to underscores (e.g. `X-Hub-Signature-256` becomes `WH_HEADER_X_HUB_SIGNATURE_256`).

The **request body** is piped to the script's **stdin**.

## Response Behavior

| Command result | HTTP response |
|---|---|
| Exit code 0 | `200 OK` with stdout as body |
| Exit code non-zero | `500 Internal Server Error` with stderr as body |
| Timeout exceeded | `504 Gateway Timeout` |

## Installing Tools for Handler Scripts

The root filesystem is immutable (ostree), so you can't install packages directly. Use nix to install packages:

```bash
# Install a package
nix profile install nixpkgs#jq
nix profile install nixpkgs#curl

# List installed packages
nix profile list

# Remove a package
nix profile remove <index>

# Free disk space
nix-collect-garbage
```

Installed binaries are automatically available in PATH.

### Distrobox alternative

You can also use distrobox to create a mutable container for tools:

```bash
# Create the distrobox (one-time)
distrobox-create --name dev --image fedora:latest
distrobox-enter --name dev -- sudo dnf install jq curl

# Export each binary you need
distrobox-enter --name dev -- distrobox-export --bin /usr/bin/jq --export-path ~/.local/bin
distrobox-enter --name dev -- distrobox-export --bin /usr/bin/curl --export-path ~/.local/bin
```

Or use `distrobox-enter` directly in your handler scripts:

```bash
#!/bin/bash
set -e
BODY=$(cat)
PAYLOAD=$(distrobox-enter --name dev -- jq -r '.ref' <<< "$BODY")
echo "Ref: $PAYLOAD"
```

This is simpler but adds overhead since each invocation spins up a distrobox session.

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

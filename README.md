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
podman logs wh-gateway | head -10

# 4. SSH in if needed
ssh -p 2222 gateway@localhost
```

## Project Structure

```
config/           → gateway.yaml, cloudflared.yaml (bind-mounted into container)
workspace/        → Your handler scripts (bind-mounted into container)
docs/             → Documentation (COPY'd into container image)
gateway/          → Go application source
systemd/          → Container systemd unit files
scripts/          → First-boot setup script
Containerfile     → Multi-stage container build
compose.yaml      → podman-compose definition
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

### cloudflared.yaml

Routes external traffic from your Cloudflare tunnel to the gateway.

```yaml
tunnel: <your-tunnel-id>
credentials-file: /home/lainos/config/cloudflared/credentials.json
ingress:
  - hostname: webhooks.example.com
    service: http://localhost:8080
  - service: http_status:404
```

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
ssh -p 2222 gateway@localhost
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

### Response behavior

| Command result | HTTP response |
|---|---|
| Exit code 0 | `200 OK` with stdout as body |
| Exit code non-zero | `500 Internal Server Error` with stderr as body |
| Timeout exceeded | `504 Gateway Timeout` |

## GitHub Webhook Setup

### On the GitHub side

1. Go to your repository → **Settings** → **Webhooks** → **Add webhook**
2. Set **Payload URL** to your tunnel URL + route path, e.g. `https://webhooks.example.com/webhook/github`
3. Set **Content type** to `application/json`
4. Set **Secret** to a random string (generate one with `openssl rand -hex 32`)
5. Choose which events trigger the webhook
6. Click **Add webhook**

### Verify signatures in your handler script

Signature verification happens in your script, not the gateway. The `X-Hub-Signature-256` header is passed as `WH_HEADER_X_HUB_SIGNATURE_256`. See `workspace/examples/handle-github.sh.example` for a complete example using `openssl`:

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
BODY=$(cat)

if [ -z "$WH_HEADER_X_HUB_SIGNATURE_256" ]; then
  echo "missing X-Hub-Signature-256 header" >&2
  exit 1
fi

EXPECTED="sha256=$(echo -n "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')"

if [ "$WH_HEADER_X_HUB_SIGNATURE_256" != "$EXPECTED" ]; then
  echo "invalid signature" >&2
  exit 1
fi
```

Since the gateway returns stderr as the HTTP body on non-zero exit, invalid signatures will return `500` with the error message.

## Cloudflare Tunnel Setup

cloudflared is already installed inside the container. All setup commands run via `podman exec` or SSH.

### Option A: Create a new tunnel from scratch

#### 1. Build and start the container

```bash
podman-compose up -d
```

The cloudflared service will fail at first because there's no config yet — that's expected.

#### 2. Authenticate with Cloudflare

```bash
podman exec wh-gateway cloudflared tunnel login
```

This prints a URL. Open it in a browser and authorize. cloudflared saves an auth certificate to `/root/.cloudflared/cert.pem` inside the container.

#### 3. Create a tunnel

```bash
podman exec wh-gateway cloudflared tunnel create wh-gateway
```

This outputs a tunnel ID and **automatically generates a credentials file** at `/root/.cloudflared/<tunnel-id>.json` inside the container. This file contains the private key for your tunnel and cannot be regenerated — don't lose it.

#### 4. Copy credentials to the bind-mounted config directory

```bash
mkdir -p config/cloudflared
podman exec wh-gateway cp /root/.cloudflared/<tunnel-id>.json /home/lainos/config/cloudflared/credentials.json
```

Since `/home/lainos/config` is bind-mounted to `./config/` on the host, this file persists across container restarts.

#### 5. Configure DNS

Point your domain to the tunnel:

```bash
podman exec wh-gateway cloudflared tunnel route dns wh-gateway webhooks.example.com
```

### Option B: Use an existing tunnel

If you already have a tunnel created through the [Cloudflare Zero Trust dashboard](https://one.dash.cloudflare.com):

1. Go to **Zero Trust** → **Networks** → **Tunnels** → your tunnel → **Configure**
2. Download the credentials JSON file
3. Place it on the host at `config/cloudflared/credentials.json`
4. Note your tunnel ID from the dashboard

### Write config/cloudflared.yaml

On the host, edit `config/cloudflared.yaml` — replace the tunnel ID with your own:

```yaml
tunnel: <your-tunnel-id>
credentials-file: /home/lainos/config/cloudflared/credentials.json
ingress:
  - hostname: webhooks.example.com
    service: http://localhost:8080
  - service: http_status:404
```

### Start cloudflared

```bash
podman exec wh-gateway systemctl restart cloudflared
```

Your gateway is now reachable at `https://webhooks.example.com`. Both the gateway and cloudflared start automatically on container boot via systemd.

## SSH Access

The container generates a random password on first boot. Retrieve it from the logs:

```bash
podman logs wh-gateway | head -10
```

Then connect:

```bash
ssh -p 2222 gateway@localhost
```

### Installing tools for handler scripts

The root filesystem is immutable (ostree), so you can't install packages directly. Distrobox lets you create a mutable container for tools — but handler scripts run on the host, not inside distrobox. You have two options:

#### Option 1: Export binaries from distrobox

Install tools in a distrobox, then export individual binaries so they're available on the host path:

```bash
# Create the distrobox (one-time)
distrobox-create --name dev --image fedora:latest
distrobox-enter --name dev -- sudo dnf install jq curl

# Export each binary you need
distrobox-enter --name dev -- distrobox-export --bin /usr/bin/jq --export-path ~/.local/bin
distrobox-enter --name dev -- distrobox-export --bin /usr/bin/curl --export-path ~/.local/bin
```

Exported binaries are wrapper scripts that transparently run inside the distrobox. Handler scripts can call them like any normal command.

#### Option 2: Use distrobox-enter in your handler scripts

Wrap commands directly in your handler script:

```bash
#!/bin/bash
set -e
BODY=$(cat)
PAYLOAD=$(distrobox-enter --name dev -- jq -r '.ref' <<< "$BODY")
echo "Ref: $PAYLOAD"
```

This is simpler but adds overhead since each invocation spins up a distrobox session.

## Container Management

```bash
# Build (or rebuild after code changes)
podman-compose build

# Start
podman-compose up -d

# Rebuild and restart
podman-compose up -d --build

# View logs
podman logs -f wh-gateway

# Stop
podman-compose down

# Restart the gateway service inside the container
ssh -p 2222 gateway@localhost
sudo systemctl restart wh-gateway
```

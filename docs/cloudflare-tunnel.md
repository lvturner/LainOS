# Cloudflare Tunnel Setup

Cloudflared is installed inside the container. It routes external traffic from your Cloudflare tunnel to the gateway on `localhost:8080`. All setup commands run via `podman exec` or SSH.

## Option A: Create a New Tunnel

### 1. Build and start the container

```bash
podman-compose up -d
```

The cloudflared service will fail at first because there's no config yet — that's expected.

### 2. Authenticate with Cloudflare

```bash
podman exec lainos cloudflared tunnel login
```

This prints a URL. Open it in a browser and authorize. Cloudflared saves an auth certificate to `/root/.cloudflared/cert.pem` inside the container.

### 3. Create a tunnel

```bash
podman exec lainos cloudflared tunnel create wh-gateway
```

This outputs a tunnel ID and automatically generates a credentials file at `/root/.cloudflared/<tunnel-id>.json` inside the container. This file contains the private key for your tunnel and cannot be regenerated — don't lose it.

### 4. Copy credentials to the bind-mounted config directory

```bash
mkdir -p config/cloudflared
podman exec lainos cp /root/.cloudflared/<tunnel-id>.json /home/lainos/config/cloudflared/credentials.json
```

Since `/home/lainos/config` is bind-mounted to `./config/` on the host, this file persists across container restarts.

### 5. Configure DNS

Point your domain to the tunnel:

```bash
podman exec lainos cloudflared tunnel route dns wh-gateway webhooks.example.com
```

## Option B: Use an Existing Tunnel

If you already have a tunnel created through the [Cloudflare Zero Trust dashboard](https://one.dash.cloudflare.com):

1. Go to **Zero Trust** → **Networks** → **Tunnels** → your tunnel → **Configure**
2. Download the credentials JSON file
3. Place it on the host at `config/cloudflared/credentials.json`
4. Note your tunnel ID from the dashboard

## Write config/cloudflared.yaml

On the host, edit `config/cloudflared.yaml` — replace the tunnel ID with your own:

```yaml
tunnel: <your-tunnel-id>
credentials-file: /home/lainos/config/cloudflared/credentials.json
ingress:
  - hostname: webhooks.example.com
    service: http://localhost:8080
  - service: http_status:404
```

The final `http_status:404` catch-all rule is required by cloudflared.

## Start Cloudflared

```bash
podman exec lainos systemctl restart cloudflared
```

Your gateway is now reachable at `https://webhooks.example.com`. Both the gateway and cloudflared start automatically on container boot via systemd.

## Adding Multiple Hostnames

To route multiple domains to different paths on the same gateway, add ingress rules:

```yaml
ingress:
  - hostname: webhooks.example.com
    service: http://localhost:8080
  - hostname: hooks.anotherdomain.com
    service: http://localhost:8080
  - service: http_status:404
```

Both hostnames hit the same gateway. Use path-based routing in `gateway.yaml` to differentiate.

After editing `cloudflared.yaml`, restart cloudflared:

```bash
podman exec lainos systemctl restart cloudflared
```

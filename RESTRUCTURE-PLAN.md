# Restructure Plan: Home Directory Migration + Examples + Docs

## Objective

1. Move `config/` and `workspace/` container mount points under `/home/lainos/`
2. Create a `docs/` directory that is COPY'd into the container image
3. Add full-featured GitHub webhook handler examples (sync + async)
4. Update all references across the entire codebase

---

## 1. New Container Path Mapping

| Host path | Container path (old) | Container path (new) |
|---|---|---|
| `./config/` | `/config/` | `/home/lainos/config/` |
| `./workspace/` | `/workspace/` | `/home/lainos/workspace/` |
| `./docs/` | _(none)_ | `/home/lainos/docs/` (COPY'd, not bind-mounted) |
| `./config/lain/` | `/home/lainos/.config/lain/` | `/home/lainos/.config/lain/` (unchanged) |

The `docs/` directory is **COPY'd into the image** via `Containerfile`, not a bind mount. Any new files added to `./docs/` on the host are included on the next `podman-compose build`. No compose changes needed when adding documentation later.

---

## 2. Files to Create (10 new files)

### 2.1 Documentation

| # | File | Description |
|---|---|---|
| 1 | `docs/README.md` | Copy of project README.md |

### 2.2 Shared helper library

| # | File | Description |
|---|---|---|
| 2 | `workspace/examples/lib/verify-github-signature.sh` | Sourceable HMAC-SHA256 signature verification helper |

This helper is sourced by all GitHub webhook examples to avoid duplicating verification logic:

```bash
#!/bin/bash
# Usage: source /home/lainos/workspace/scripts/lib/verify-github-signature.sh
# Expects: SECRET env var set to the webhook secret
# Reads: stdin (body)
# Sets: BODY variable
# Exits 1 with stderr message if verification fails

if [ -z "${SECRET:-}" ]; then
  echo "SECRET env var not set" >&2
  exit 1
fi

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

### 2.3 Example handlers (sync + async pairs)

Each example includes:
- HMAC-SHA256 signature verification (via shared helper)
- JSON parsing with `jq`
- Event dispatching based on `$WH_HEADER_X_GITHUB_EVENT`
- Realistic placeholder logic
- Proper exit codes and stderr for error responses

**Sync** = validate, process, respond with result
**Async** = validate, fork background job, respond immediately with acknowledgment

#### Async pattern

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"
# Parse minimal info before forking
SOME_FIELD=$(echo "$BODY" | jq -r '.some.field')

# Fork long-running work into background
(
  # ... real work goes here ...
  sleep 30
) >/home/lainos/workspace/logs/"${EVENT}-$(date +%s).log" 2>&1 &

# Return immediately — gateway sends stdout as HTTP 200
echo "accepted: ${EVENT}"
```

Key characteristics of the async pattern:
- Signature verification happens synchronously (before fork) — invalid signatures return 500
- Body is captured before forking — stdin consumed in parent process
- Background work writes to `/home/lainos/workspace/logs/` so output is never lost
- Log path includes event type + timestamp for identification
- Parent exits 0 immediately — gateway returns 200 with stdout

#### File listing

| # | File | Event | Sync action | Async forks |
|---|---|---|---|---|
| 3 | `workspace/examples/handle-github-push.sh.example` | push | Parse commits, log ref/branch, respond with summary | — |
| 4 | `workspace/examples/handle-github-push-async.sh.example` | push | — | Fork: git pull + build pipeline |
| 5 | `workspace/examples/handle-github-issues.sh.example` | issues | Parse issue action/title/body, respond with acknowledgment | — |
| 6 | `workspace/examples/handle-github-issues-async.sh.example` | issues | — | Fork: notification dispatch |
| 7 | `workspace/examples/handle-github-pr.sh.example` | pull_request | Parse PR action/branch/head, run quick check, respond | — |
| 8 | `workspace/examples/handle-github-pr-async.sh.example` | pull_request | — | Fork: full CI suite |
| 9 | `workspace/examples/handle-github-release.sh.example` | release | Parse tag/assets, respond with release info | — |
| 10 | `workspace/examples/handle-github-release-async.sh.example` | release | — | Fork: artifact deployment |

---

## 3. Files to Modify (11 files)

### 3.1 Container & orchestration

#### `compose.yaml`

```yaml
services:
  lainos:
    build: .
    container_name: lainos
    privileged: true
    dns:
      - 1.1.1.1
      - 8.8.8.8
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

Changes:
- `./config:/config:Z` → `./config:/home/lainos/config:Z`
- `./workspace:/workspace:Z` → `./workspace:/home/lainos/workspace:Z`
- lain config mount unchanged

#### `Containerfile`

```dockerfile
FROM golang:1.23 AS builder
WORKDIR /build
COPY gateway/ .
RUN CGO_ENABLED=0 go build -o wh-gateway .

FROM golang:1.25 AS lain-builder
WORKDIR /build
COPY lain/ .
RUN CGO_ENABLED=0 go build -o lain .

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

Changes:
- `mkdir -p /config /workspace` → `mkdir -p /home/lainos/config /home/lainos/workspace /home/lainos/workspace/logs`
- `chown` paths updated
- Added `COPY docs/ /home/lainos/docs/`
- `VOLUME` declaration updated
- Docs dir is COPY'd (not a volume), so new files in `./docs/` are picked up on rebuild automatically

### 3.2 Systemd services

#### `systemd/wh-gateway.service`

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

Change: `-config /config/gateway.yaml` → `-config /home/lainos/config/gateway.yaml`

#### `systemd/cloudflared.service`

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

Change: `--config /config/cloudflared.yaml` → `--config /home/lainos/config/cloudflared.yaml`

### 3.3 Example configs

#### `config/gateway.example.yaml`

```yaml
server:
  listen: "0.0.0.0:8080"
  timeout: 30s

routes:
  - path: "/webhook/github"
    command: "/home/lainos/workspace/scripts/handle-github.sh"
    method: "POST"

  - path: "/webhook/github/async"
    command: "/home/lainos/workspace/scripts/handle-github-push-async.sh"
    method: "POST"

  - path: "/webhook/deploy"
    command: "/home/lainos/workspace/scripts/deploy.sh"
    method: "POST"
```

Change: All `/workspace/` command paths → `/home/lainos/workspace/`
Added: Async route example

#### `config/cloudflared.example.yaml`

```yaml
tunnel: <your-tunnel-id>
credentials-file: /home/lainos/config/cloudflared/credentials.json
ingress:
  - hostname: webhooks.example.com
    service: http://localhost:8080
  - service: http_status:404
```

Change: `credentials-file: /config/cloudflared/credentials.json` → `/home/lainos/config/cloudflared/credentials.json`

### 3.4 Startup script

#### `start.sh`

All `config/` references in ensure_config and lain setup stay as-is (they refer to host-side paths). The script does not need changes for the host-side operations. However, verify that any container-exec commands reference the new internal paths. Specifically:

- `podman exec lainos cp /root/.cloudflared/<tunnel-id>.json /config/cloudflared/credentials.json` → change to `/home/lainos/config/cloudflared/credentials.json` (if present in start.sh — currently it's only in README)

The `ensure_config` function operates on host paths (`config/gateway.example.yaml` → `config/gateway.yaml`) which are unchanged. The lain profile setup also uses host-side paths. No changes needed to start.sh itself.

### 3.5 Documentation files

#### `README.md`

All path references throughout:
- `/config/` → `/home/lainos/config/`
- `/workspace/` → `/home/lainos/workspace/`
- `/config/cloudflared/credentials.json` → `/home/lainos/config/cloudflared/credentials.json`
- Project structure section: add `docs/` entry
- "Registering a New Webhook" section: update command paths
- "Cloudflare Tunnel Setup" section: update credential copy paths
- `podman exec wh-gateway cp /root/.cloudflared/<tunnel-id>.json /config/cloudflared/credentials.json` → use `/home/lainos/config/cloudflared/credentials.json`

#### `PLAN.md`

Same path updates throughout:
- Project structure section
- Containerfile section
- compose.yaml section
- Volume table
- Systemd service sections
- Data flow diagram
- Config examples

#### `AGENTS.md`

Update:
- Volume table to reflect new container paths
- Systemd restart command paths
- Any references to `/config/` or `/workspace/`

### 3.6 Existing example

#### `workspace/examples/handle-github.sh.example`

Update the env var reference comments to note the new path structure. The actual script logic doesn't change since env vars are set by the gateway, not paths.

---

## 4. Example Handler Details

### handle-github-push.sh.example (sync)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "push" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

REF=$(echo "$BODY" | jq -r '.ref')
BEFORE=$(echo "$BODY" | jq -r '.before')
AFTER=$(echo "$BODY" | jq -r '.after')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')
BRANCH=$(echo "$REF" | sed 's|refs/heads/||')
COMMIT_COUNT=$(echo "$BODY" | jq '.commits | length')
COMMIT_MESSAGES=$(echo "$BODY" | jq -r '.commits[]?.message // empty' | head -5)

echo "Push to ${REPO}:${BRANCH}"
echo "Commits: ${COMMIT_COUNT}"
echo "Range: ${BEFORE:0:7}..${AFTER:0:7}"
echo ""
echo "Messages:"
echo "$COMMIT_MESSAGES"

# Placeholder: trigger a build or deploy
# cd /home/lainos/workspace/repos/"${REPO}" && git pull && make build
```

### handle-github-push-async.sh.example (async)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "push" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

REF=$(echo "$BODY" | jq -r '.ref')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')
BRANCH=$(echo "$REF" | sed 's|refs/heads/||')

(
  sleep 5

  AFTER=$(echo "$BODY" | jq -r '.after')
  COMMIT_COUNT=$(echo "$BODY" | jq '.commits | length')

  echo "Starting build for ${REPO}:${BRANCH}"
  echo "Commits: ${COMMIT_COUNT}"
  echo "Head: ${AFTER:0:7}"

  # Placeholder: clone/pull repo and run build
  # mkdir -p /home/lainos/workspace/repos/"${REPO}"
  # cd /home/lainos/workspace/repos/"${REPO}"
  # git pull origin "${BRANCH}"
  # make build
  # make deploy

  echo "Build complete for ${REPO}:${BRANCH}"
) >/home/lainos/workspace/logs/"push-${REPO//\//-}-${BRANCH}-$(date +%s).log" 2>&1 &

echo "accepted: push to ${REPO}:${BRANCH}"
```

### handle-github-issues.sh.example (sync)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "issues" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

ACTION=$(echo "$BODY" | jq -r '.action')
ISSUE_NUMBER=$(echo "$BODY" | jq -r '.issue.number')
ISSUE_TITLE=$(echo "$BODY" | jq -r '.issue.title')
ISSUE_USER=$(echo "$BODY" | jq -r '.issue.user.login')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')

echo "Issue #${ISSUE_NUMBER} ${ACTION} by ${ISSUE_USER}"
echo "Title: ${ISSUE_TITLE}"
echo "Repo: ${REPO}"

if [ "$ACTION" = "opened" ]; then
  echo ""
  echo "Body preview:"
  echo "$BODY" | jq -r '.issue.body' | head -10
  # Placeholder: send notification, auto-label, assign
elif [ "$ACTION" = "closed" ]; then
  # Placeholder: log metrics, trigger cleanup
  echo "Issue closed"
fi
```

### handle-github-issues-async.sh.example (async)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "issues" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

ACTION=$(echo "$BODY" | jq -r '.action')
ISSUE_NUMBER=$(echo "$BODY" | jq -r '.issue.number')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')

(
  sleep 2

  ISSUE_TITLE=$(echo "$BODY" | jq -r '.issue.title')
  ISSUE_USER=$(echo "$BODY" | jq -r '.issue.user.login')
  ISSUE_BODY=$(echo "$BODY" | jq -r '.issue.body')

  echo "Processing issue #${ISSUE_NUMBER} ${ACTION}"
  echo "Title: ${ISSUE_TITLE}"
  echo "By: ${ISSUE_USER}"

  if [ "$ACTION" = "opened" ]; then
    # Placeholder: triage issue with LLM
    # echo "$ISSUE_BODY" | classify-and-label.sh "${REPO}" "${ISSUE_NUMBER}"
    echo "Triaging new issue"
  elif [ "$ACTION" = "closed" ]; then
    echo "Issue closed - updating metrics"
  fi

  echo "Done processing issue #${ISSUE_NUMBER}"
) >/home/lainos/workspace/logs/"issue-${REPO//\//--}-${ISSUE_NUMBER}-$(date +%s).log" 2>&1 &

echo "accepted: issue #${ISSUE_NUMBER} ${ACTION}"
```

### handle-github-pr.sh.example (sync)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "pull_request" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

ACTION=$(echo "$BODY" | jq -r '.action')
PR_NUMBER=$(echo "$BODY" | jq -r '.number')
PR_TITLE=$(echo "$BODY" | jq -r '.pull_request.title')
PR_USER=$(echo "$BODY" | jq -r '.pull_request.user.login')
PR_BRANCH=$(echo "$BODY" | jq -r '.pull_request.head.ref')
PR_BASE=$(echo "$BODY" | jq -r '.pull_request.base.ref')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')
MERGEABLE=$(echo "$BODY" | jq -r '.pull_request.mergeable // "unknown"')

echo "PR #${PR_NUMBER} ${ACTION} by ${PR_USER}"
echo "Title: ${PR_TITLE}"
echo "Branch: ${PR_BRANCH} → ${PR_BASE}"
echo "Mergeable: ${MERGEABLE}"

if [ "$ACTION" = "opened" ] || [ "$ACTION" = "synchronize" ]; then
  # Placeholder: quick lint/style check
  echo "Running quick checks..."
  # cd /home/lainos/workspace/repos/"${REPO}" && git fetch origin "${PR_BRANCH}" && make lint
fi
```

### handle-github-pr-async.sh.example (async)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "pull_request" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

ACTION=$(echo "$BODY" | jq -r '.action')
PR_NUMBER=$(echo "$BODY" | jq -r '.number')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')

(
  sleep 2

  PR_TITLE=$(echo "$BODY" | jq -r '.pull_request.title')
  PR_BRANCH=$(echo "$BODY" | jq -r '.pull_request.head.ref')
  PR_BASE=$(echo "$BODY" | jq -r '.pull_request.base.ref')
  PR_SHA=$(echo "$BODY" | jq -r '.pull_request.head.sha')
  CLONE_URL=$(echo "$BODY" | jq -r '.pull_request.head.repo.clone_url')

  echo "Running CI for PR #${PR_NUMBER}: ${PR_BRANCH} → ${PR_BASE}"
  echo "SHA: ${PR_SHA}"
  echo "Clone: ${CLONE_URL}"

  if [ "$ACTION" = "opened" ] || [ "$ACTION" = "synchronize" ]; then
    # Placeholder: full CI pipeline
    # WORKDIR=$(mktemp -d)
    # git clone "${CLONE_URL}" "${WORKDIR}"
    # cd "${WORKDIR}" && git checkout "${PR_SHA}"
    # make test
    # make lint
    # make build
    # rm -rf "${WORKDIR}"
    echo "CI pipeline would run here"
  elif [ "$ACTION" = "closed" ]; then
    echo "PR closed - cleaning up CI resources"
  fi

  echo "Done with PR #${PR_NUMBER}"
) >/home/lainos/workspace/logs/"pr-${REPO//\//--}-${PR_NUMBER}-$(date +%s).log" 2>&1 &

echo "accepted: PR #${PR_NUMBER} ${ACTION}"
```

### handle-github-release.sh.example (sync)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "release" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

ACTION=$(echo "$BODY" | jq -r '.action')
TAG=$(echo "$BODY" | jq -r '.release.tag_name')
NAME=$(echo "$BODY" | jq -r '.release.name // empty')
DRAFT=$(echo "$BODY" | jq -r '.release.draft')
PRERELEASE=$(echo "$BODY" | jq -r '.release.prerelease')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')
AUTHOR=$(echo "$BODY" | jq -r '.release.author.login')

echo "Release ${ACTION}: ${TAG}"
echo "Repo: ${REPO}"
echo "By: ${AUTHOR}"
echo "Draft: ${DRAFT}, Pre-release: ${PRERELEASE}"

if [ -n "$NAME" ]; then
  echo "Name: ${NAME}"
fi

ASSET_COUNT=$(echo "$BODY" | jq '.release.assets | length')
echo "Assets: ${ASSET_COUNT}"

if [ "$ACTION" = "published" ]; then
  echo "$BODY" | jq -r '.release.assets[]? | "\(.name) \(.size) \(.browser_download_url)"'
  # Placeholder: notify channels, update changelog
fi
```

### handle-github-release-async.sh.example (async)

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/scripts/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "release" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

ACTION=$(echo "$BODY" | jq -r '.action')
TAG=$(echo "$BODY" | jq -r '.release.tag_name')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')

(
  sleep 2

  DRAFT=$(echo "$BODY" | jq -r '.release.draft')
  PRERELEASE=$(echo "$BODY" | jq -r '.release.prerelease')
  AUTHOR=$(echo "$BODY" | jq -r '.release.author.login')

  echo "Processing release ${ACTION}: ${TAG}"
  echo "Repo: ${REPO}"
  echo "By: ${AUTHOR}"

  if [ "$ACTION" = "published" ] && [ "$DRAFT" = "false" ]; then
    ASSETS=$(echo "$BODY" | jq -r '.release.assets[]? | .browser_download_url')

    echo "Downloading assets..."
    # DOWNLOAD_DIR="/home/lainos/workspace/releases/${REPO}/${TAG}"
    # mkdir -p "${DOWNLOAD_DIR}"
    # for url in $ASSETS; do
    #   curl -sL "$url" -o "${DOWNLOAD_DIR}/$(basename "$url")"
    # done

    echo "Deploying release ${TAG}..."
    # Placeholder: deploy artifact, update services
    # make deploy VERSION="${TAG}"

    echo "Release ${TAG} deployed"
  fi

  echo "Done processing release ${TAG}"
) >/home/lainos/workspace/logs/"release-${REPO//\//--}-${TAG}-$(date +%s).log" 2>&1 &

echo "accepted: release ${ACTION} ${TAG}"
```

---

## 5. Execution Order

1. Create `docs/` directory with `docs/README.md` (copy of README.md)
2. Create `workspace/examples/lib/verify-github-signature.sh`
3. Create 8 example handler files (4 sync + 4 async)
4. Update `Containerfile` (paths + docs COPY + logs dir)
5. Update `compose.yaml` (volume targets)
6. Update `systemd/wh-gateway.service` (config path)
7. Update `systemd/cloudflared.service` (config path)
8. Update `config/gateway.example.yaml` (command paths + async route)
9. Update `config/cloudflared.example.yaml` (credentials path)
10. Update `start.sh` (verify no container-exec path references needed)
11. Update `README.md` (all path references)
12. Update `PLAN.md` (all path references)
13. Update `AGENTS.md` (volume table + paths)
14. Update existing `workspace/examples/handle-github.sh.example` (minor: add note about lib helper)
15. Verify Go build: `cd gateway && go build ./...`

---

## 6. Summary of All Path Changes

| Old path | New path |
|---|---|
| `/config/` | `/home/lainos/config/` |
| `/config/gateway.yaml` | `/home/lainos/config/gateway.yaml` |
| `/config/cloudflared.yaml` | `/home/lainos/config/cloudflared.yaml` |
| `/config/cloudflared/credentials.json` | `/home/lainos/config/cloudflared/credentials.json` |
| `/workspace/` | `/home/lainos/workspace/` |
| `/workspace/scripts/` | `/home/lainos/workspace/scripts/` |
| _(none)_ | `/home/lainos/workspace/logs/` |
| _(none)_ | `/home/lainos/docs/` |

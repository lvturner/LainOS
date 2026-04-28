# Webhook Handler Examples

Example scripts live in `/home/lainos/workspace/examples/`. Copy them to `/home/lainos/workspace/scripts/`, remove the `.example` suffix, and customize as needed.

## Shared Library: Signature Verification

`/home/lainos/workspace/examples/lib/verify-github-signature.sh` verifies the `X-Hub-Signature-256` header using HMAC-SHA256. Source it at the top of any GitHub webhook handler:

```bash
SECRET="your-webhook-secret"
source /home/lainos/workspace/examples/lib/verify-github-signature.sh
```

It expects:
- `SECRET` env var set to your GitHub webhook secret
- `WH_HEADER_X_HUB_SIGNATURE_256` set by the gateway (from the incoming request header)
- The request body on stdin

It reads stdin into `$BODY`, verifies the signature, and exits non-zero on failure (returning HTTP 500 to the caller).

## GitHub Webhook Setup

On the GitHub side:

1. Go to your repository → **Settings** → **Webhooks** → **Add webhook**
2. Set **Payload URL** to your tunnel URL + route path, e.g. `https://webhooks.example.com/webhook/github`
3. Set **Content type** to `application/json`
4. Set **Secret** to a random string (generate with `openssl rand -hex 32`)
5. Choose which events trigger the webhook
6. Click **Add webhook**

## Basic Handler

`handle-github.sh.example` — Verifies the signature and prints webhook details:

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/examples/lib/verify-github-signature.sh

echo "Received GitHub webhook"
echo "Method: $WH_METHOD"
echo "Path:   $WH_PATH"
echo "Event:  $WH_HEADER_X_GITHUB_EVENT"
echo "Body: $BODY"
```

## Push Handler

`handle-github-push.sh.example` — Handles push events, extracts branch, commit range, and messages. Requires `jq`.

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/examples/lib/verify-github-signature.sh

EVENT="$WH_HEADER_X_GITHUB_EVENT"

if [ "$EVENT" != "push" ]; then
  echo "unexpected event: $EVENT" >&2
  exit 1
fi

REF=$(echo "$BODY" | jq -r '.ref')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')
BRANCH=$(echo "$REF" | sed 's|refs/heads/||')
COMMIT_COUNT=$(echo "$BODY" | jq '.commits | length')

echo "Push to ${REPO}:${BRANCH}"
echo "Commits: ${COMMIT_COUNT}"
```

## Async Handler Pattern

`handle-github-push-async.sh.example` — Returns immediately (HTTP 200) while processing in the background. Logs output to `/home/lainos/workspace/logs/`.

```bash
#!/bin/bash
set -e

SECRET="your-webhook-secret"
source /home/lainos/workspace/examples/lib/verify-github-signature.sh

REF=$(echo "$BODY" | jq -r '.ref')
REPO=$(echo "$BODY" | jq -r '.repository.full_name')
BRANCH=$(echo "$REF" | sed 's|refs/heads/||')

(
  sleep 5
  # Long-running work here (builds, deployments, etc.)
  echo "Build complete for ${REPO}:${BRANCH}"
) >/home/lainos/workspace/logs/"push-${REPO//\//-}-${BRANCH}-$(date +%s).log" 2>&1 &

echo "accepted: push to ${REPO}:${BRANCH}"
```

The subshell runs in the background (`&`), so the gateway gets an immediate response. Logs are written to `/home/lainos/workspace/logs/` for later review.

## Issue Handler

`handle-github-issues.sh.example` — Handles issue opened/closed events:

```bash
ACTION=$(echo "$BODY" | jq -r '.action')
ISSUE_NUMBER=$(echo "$BODY" | jq -r '.issue.number')
ISSUE_TITLE=$(echo "$BODY" | jq -r '.issue.title')
ISSUE_USER=$(echo "$BODY" | jq -r '.issue.user.login')

echo "Issue #${ISSUE_NUMBER} ${ACTION} by ${ISSUE_USER}"
echo "Title: ${ISSUE_TITLE}"
```

## Pull Request Handler

`handle-github-pr.sh.example` — Handles PR opened/synchronize events:

```bash
ACTION=$(echo "$BODY" | jq -r '.action')
PR_NUMBER=$(echo "$BODY" | jq -r '.number')
PR_TITLE=$(echo "$BODY" | jq -r '.pull_request.title')
PR_BRANCH=$(echo "$BODY" | jq -r '.pull_request.head.ref')
PR_BASE=$(echo "$BODY" | jq -r '.pull_request.base.ref')

echo "PR #${PR_NUMBER} ${ACTION}"
echo "Branch: ${PR_BRANCH} → ${PR_BASE}"
```

## Release Handler

`handle-github-release.sh.example` — Handles release published/created events, extracts tag, assets, and metadata:

```bash
TAG=$(echo "$BODY" | jq -r '.release.tag_name')
DRAFT=$(echo "$BODY" | jq -r '.release.draft')
PRERELEASE=$(echo "$BODY" | jq -r '.release.prerelease')

echo "Release ${ACTION}: ${TAG}"

if [ "$ACTION" = "published" ]; then
  echo "$BODY" | jq -r '.release.assets[]? | "\(.name) \(.size) \(.browser_download_url)"'
fi
```

## Tips

- Always set `SECRET` to the same value you configured in the GitHub webhook settings
- The shared lib sets `$BODY` from stdin — don't read stdin again after sourcing it
- Use `jq` to parse the JSON payload (install via `nix profile install nixpkgs#jq`)
- For async handlers, use the subshell + `&` pattern to avoid blocking the gateway
- Log files go in `/home/lainos/workspace/logs/` which persists on the bind-mounted volume

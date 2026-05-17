# Plan: Security Hardening

## Problem

Several security issues expose credentials and weaken the container's isolation.

## Issues

### 1. SSH password leaked in container logs (HIGH)

**File:** `scripts/first-boot-setup.sh:43-46`

The `first-boot-setup.sh` script echoes the SSH password to stdout, which is captured by `podman logs lainos`. Anyone with access to `podman logs` on the host can read the password.

```bash
echo "============================================"
echo " lainos SSH credentials:"
echo "   User:     lainos"
echo "   Password: $PASSWORD"   # <-- leaked to podman logs
echo "   Port:     22"
echo "============================================"
```

**Fix:** Send the banner to stderr or to a file, not stdout. The password is already saved to `/var/lib/lainos/.password` for retrieval via `podman exec`.

```bash
echo "SSH credentials saved to /var/lib/lainos/.password" 
```

The `start.sh` script already retrieves it via `podman exec lainos cat /var/lib/lainos/.password` — the banner in stdout is redundant. Remove the password from stdout entirely, or redirect to stderr:

```bash
cat >&2 <<EOF
============================================
 lainos SSH credentials:
   User:     lainos
   Password: $PASSWORD
   Port:     22
============================================
EOF
```

`podman logs` captures both stdout and stderr by default, so the better fix is to remove the password from the banner entirely and only write it to the file.

### 2. API key stored as plaintext with no permission hardening (MEDIUM)

**File:** `start.sh:168-180`

The API key is written to `.data/home/.config/lain/profiles/default/config.yaml` as plaintext YAML. No `chmod` is applied after writing.

**Fix:** Add `chmod 600` after writing the config:

```bash
chmod 600 "$LAIN_DIR/config.yaml"
```

### 3. `start.sh` mutates tracked `Containerfile` (MEDIUM)

**File:** `start.sh:226`

```bash
sed -i "s/-u [0-9]\+/-u ${HOST_UID}/" Containerfile
```

This silently modifies a git-tracked file to match the host UID. It creates confusing `git diff` output and could accidentally be committed.

**Fix:** Use a build arg instead of mutating the file:

In `Containerfile`, replace:
```dockerfile
ARG HOST_UID=1000
RUN useradd -m -u ${HOST_UID} -s /bin/bash lainos
```

In `compose.override.yaml`, inject the build arg:
```yaml
services:
  lainos:
    build:
      args:
        HOST_UID: "1000"
```

Then `start.sh` writes the UID to the override file instead of sed-mutating the Containerfile.

### 4. No webhook signature verification in gateway (LOW)

The gateway executes arbitrary commands from route config with no request authentication. While signature verification is the handler script's responsibility (see `examples/lib/verify-github-signature.sh`), the gateway itself provides no request-level auth (API key, HMAC, IP allowlist).

**Fix (optional, documented):** Add optional per-route secret validation in `gateway.yaml`:

```yaml
routes:
  - path: "/webhook/github"
    command: "/home/lainos/workspace/scripts/handle-github.sh"
    method: "POST"
    secret: "${WEBHOOK_SECRET}"  # validates X-Hub-Signature-256
```

In `handler.go`, if `route.Secret` is set, validate the HMAC before executing the command. This prevents unauthenticated requests from reaching handler scripts.

This is optional because users can implement it in their handler scripts, but a built-in option reduces the chance of mistakes.

### 5. Container runs privileged (INFO)

**File:** `compose.yaml`

The container runs `privileged: true` with `network_mode: host`. This is necessary for systemd-as-PID-1 and btrfs snapshot access, but it means a compromised container has full host access.

**Mitigation (not fixable without architecture change):** Document the tradeoff. Consider whether `--cap-add SYS_ADMIN --security-opt seccomp=unconfined` with explicit capability drops would be sufficient instead of full `privileged`. This is a known limitation of the systemd-in-container pattern.

## Implementation Order

1. Remove SSH password from stdout in `first-boot-setup.sh`
2. Add `chmod 600` to lain config write in `start.sh`
3. Replace `sed -i` on Containerfile with build arg
4. (Optional) Add built-in webhook secret validation
5. (Optional) Document privileged container tradeoff

## Testing

```bash
# Verify password not in logs after rebuild
podman-compose up -d --build
podman logs lainos | grep -i password
# Should not show the actual password

# Verify config file permissions
podman exec lainos ls -la /home/lainos/.config/lain/profiles/default/config.yaml
# Should show -rw-------

# Verify Containerfile is unmodified
git diff Containerfile
# Should be empty
```

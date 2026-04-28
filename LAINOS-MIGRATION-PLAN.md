# lainos Migration Plan

## Summary

Rename the container/project from **wh-gateway** to **lainos**, add a username prompt in `start.sh`, and create the user at runtime with `/usr/local/bin/lain` as their login shell. The `gateway/` Go binary and `lain/` binary keep their names.

---

## 1. `start.sh` — Major rewrite

- Replace the ASCII art banner from "WH-GATEWAY" to "LAINOS"
- Add a **username prompt** at the top: `read -p "Username: " username`
- Write `LAINOS_USER=<username>` to a `.env` file
- Update all `podman exec` calls to reference container name `lainos` (was `wh-gateway`)
- Update lain profile setup to remain under `config/lain/profiles/default/` (host side doesn't change)
- Update the credentials display to show the dynamic username
- Change the final exec from `/bin/bash` to `/usr/local/bin/lain`:
  ```
  podman exec -it -u "$username" -w "/home/$username" lainos /usr/local/bin/lain
  ```
- Update state paths from `/var/lib/wh-gateway/` to `/var/lib/lainos/`

## 2. `compose.yaml` — Rename service + dynamic mount

- Service name: `lainos` (was `wh-gateway`)
- Container name: `lainos`
- Add `env_file: .env` so `LAINOS_USER` is available inside the container
- Change lain config volume to use variable interpolation:
  ```yaml
  - ./config/lain:/home/${LAINOS_USER}/.config/lain:Z
  ```
- All other volumes stay the same

## 3. `Containerfile` — Remove hardcoded user

- Remove the `useradd -m -s /bin/bash gateway` line entirely
- Keep `mkdir -p /config /workspace` but remove the `chown gateway:gateway` (ownership set at runtime)
- Binary names and copy paths stay the same (`wh-gateway`, `lain`)
- Service enabling stays the same

## 4. `scripts/first-boot-setup.sh` — Dynamic user creation

- Read `LAINOS_USER` from environment
- Create user: `useradd -m -s /usr/local/bin/lain "$LAINOS_USER"`
- Set password for `$LAINOS_USER` (not hardcoded `gateway`)
- `chown "$LAINOS_USER:$LAINOS_USER" /config /workspace /home/"$LAINOS_USER"/.config`
- Update state dir to `/var/lib/lainos/`
- Update display text to show "lainos" and the dynamic username

## 5. `systemd/first-boot-setup.service` — Update paths

- Change `ConditionPathExists` to `!/var/lib/lainos/.setup-complete`

## 6. `systemd/wh-gateway.service` — No change

- Stays as-is since the gateway binary keeps its name

## 7. `PLAN.md` + `AGENTS.md` — Update branding

- Replace `wh-gateway` project naming with `lainos` where it refers to the container/project
- Update references to dynamic user creation
- Note that binaries keep their original names

## 8. `.env` + `.gitignore`

- `start.sh` creates `.env` with `LAINOS_USER=<username>`
- Add `.env` to `.gitignore` (or create one) so credentials/username aren't committed

## 9. `config/gateway.yaml` + `config/gateway.example.yaml` — No change

- These are for the gateway binary config, stays the same

---

## Files changed (ordered by execution dependency)

| File | Action |
|---|---|
| `start.sh` | Rewrite: banner, username prompt, `.env` generation, dynamic exec |
| `compose.yaml` | Rename service/container, add `env_file`, dynamic lain mount |
| `Containerfile` | Remove hardcoded `useradd` and `chown` |
| `scripts/first-boot-setup.sh` | Dynamic user creation with `lain` shell |
| `systemd/first-boot-setup.service` | Update state path to `/var/lib/lainos/` |
| `.gitignore` | Add `.env` |
| `PLAN.md` | Update branding |
| `AGENTS.md` | Update branding |

## Not changed

- `gateway/` — binary and source keep the name `wh-gateway`
- `lain/` — binary and source keep the name `lain`
- `systemd/wh-gateway.service` — still runs the gateway binary
- `config/gateway.yaml` / `config/gateway.example.yaml` — gateway config unchanged
- `systemd/cloudflared.service` — unchanged

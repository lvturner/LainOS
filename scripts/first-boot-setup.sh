#!/bin/bash
set -e

if [ -z "$(ls -A /home/lainos 2>/dev/null)" ]; then
    cp -a /etc/skel-lainos/. /home/lainos/
fi

chown -R lainos:lainos /home/lainos
chown lainos:lainos /nix 2>/dev/null || true

if [ ! -e /home/lainos/.nix-profile ] || [ -L /home/lainos/.nix-profile ]; then
    ln -sf /nix/var/nix/profiles/default /home/lainos/.nix-profile
    chown -h lainos:lainos /home/lainos/.nix-profile
fi

if [ ! -e /home/lainos/.nix-defexpr ] || [ -L /home/lainos/.nix-defexpr ]; then
    ln -sf /nix/var/nix/defexpr/per-user/lainos /home/lainos/.nix-defexpr
    chown -h lainos:lainos /home/lainos/.nix-defexpr
fi

for unit in wh-gateway.service camofox.service; do
    if [ -e "/home/lainos/.config/systemd/user/$unit" ] && [ -e "/usr/lib/systemd/user/$unit" ]; then
        rm -f "/home/lainos/.config/systemd/user/$unit"
    fi
done

PASSWORD=$(openssl rand -base64 18)
echo "lainos:$PASSWORD" | chpasswd

loginctl enable-linger lainos

export XDG_RUNTIME_DIR=/run/user/1000
mkdir -p "$XDG_RUNTIME_DIR"
chown lainos:lainos "$XDG_RUNTIME_DIR"
su - lainos -s /bin/bash -c "XDG_RUNTIME_DIR=/run/user/1000 systemctl --user enable --now wh-gateway.service cloudflared.service camofox.service"

mkdir -p /var/lib/lainos
echo "$PASSWORD" > /var/lib/lainos/.password
chmod 600 /var/lib/lainos/.password
touch /var/lib/lainos/.setup-complete

echo "SSH credentials saved to /var/lib/lainos/.password"

#!/bin/bash
set -e

PASSWORD=$(openssl rand -base64 18)
echo "lainos:$PASSWORD" | chpasswd

chown -R lainos:lainos /home/lainos/.local
chown -R lainos:lainos /home/lainos/config
chown -R lainos:lainos /home/lainos/workspace
chown -R lainos:lainos /home/lainos/.config
chown lainos:lainos /nix 2>/dev/null || true

if [ ! -e /home/lainos/.nix-profile ]; then
    ln -s /nix/var/nix/profiles/default /home/lainos/.nix-profile
    chown -h lainos:lainos /home/lainos/.nix-profile
fi

if [ ! -e /home/lainos/.nix-defexpr ]; then
    ln -s /nix/var/nix/defexpr/per-user/lainos /home/lainos/.nix-defexpr
    chown -h lainos:lainos /home/lainos/.nix-defexpr
fi

loginctl enable-linger lainos

export XDG_RUNTIME_DIR=/run/user/1000
mkdir -p "$XDG_RUNTIME_DIR"
chown lainos:lainos "$XDG_RUNTIME_DIR"
su - lainos -s /bin/bash -c "XDG_RUNTIME_DIR=/run/user/1000 systemctl --user enable --now wh-gateway.service cloudflared.service"

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

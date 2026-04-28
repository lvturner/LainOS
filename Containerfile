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
    rpm-ostree install curl https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-x86_64.rpm

COPY --from=builder /build/wh-gateway /usr/local/bin/wh-gateway
COPY --from=lain-builder /build/lain /usr/local/bin/lain

RUN useradd -m -u 1000 -s /bin/bash lainos && \
    mkdir -p /home/lainos/.config /home/lainos/.local /home/lainos/config /home/lainos/workspace /home/lainos/workspace/logs && \
    chown -R lainos:lainos /home/lainos/.config /home/lainos/.local /home/lainos/config /home/lainos/workspace

RUN printf '%s\n' \
    '' \
    '  |          _)        _ \\   ___|' \
    '  |      _` | | __ \\  |   |\\___ \\' \
    '  |     (   | | |   | |   |      |' \
    ' _____|\\__,_|_|_|  _|\\___/ _____/' \
    '' \
    '  lain            Start the Lain AI shell' \
    '  lain --help     Show available options' \
    '  docs/           /home/lainos/docs/' \
    '' \
    > /etc/motd

RUN mkdir -m 0755 /nix && chown lainos:lainos /nix

USER lainos
RUN curl -L https://nixos.org/nix/install | sh -s -- --no-daemon && \
    mkdir -p /home/lainos/.config/nix && \
    echo 'experimental-features = nix-command flakes' > /home/lainos/.config/nix/nix.conf && \
    /home/lainos/.nix-profile/bin/nix-channel --add https://nixos.org/channels/nixpkgs-unstable nixpkgs && \
    /home/lainos/.nix-profile/bin/nix-channel --update && \
    sed -i 's|"\$HOME/.local/bin:\$HOME/bin:"|"\$HOME/.nix-profile/bin:\$HOME/.local/bin:\$HOME/bin:"|' /home/lainos/.bashrc && \
    sed -i 's|PATH="\$HOME/.local/bin:\$HOME/bin:\$PATH"|PATH="\$HOME/.nix-profile/bin:\$HOME/.local/bin:\$HOME/bin:\$PATH"|' /home/lainos/.bashrc && \
    sed -i '/nix\.sh/d' /home/lainos/.bash_profile
USER root

ENV PATH="/home/lainos/.nix-profile/bin:/home/lainos/.local/bin:${PATH}"
ENV NIX_PATH="nixpkgs=/home/lainos/.nix-defexpr/channels/nixpkgs"
ENV NIX_SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt

COPY docs/ /home/lainos/docs/

COPY systemd/first-boot-setup.service /usr/lib/systemd/system/
COPY systemd/98-lainos.preset /usr/lib/systemd/system-preset/

RUN mkdir -p /home/lainos/.config/systemd/user && \
    chown -R lainos:lainos /home/lainos/.config/systemd
COPY systemd/wh-gateway.service /home/lainos/.config/systemd/user/
COPY systemd/cloudflared.service /home/lainos/.config/systemd/user/
RUN chown lainos:lainos /home/lainos/.config/systemd/user/wh-gateway.service /home/lainos/.config/systemd/user/cloudflared.service

COPY scripts/first-boot-setup.sh /usr/local/bin/first-boot-setup.sh
COPY scripts/init-wrapper.sh /usr/local/bin/init-wrapper.sh
RUN chmod +x /usr/local/bin/first-boot-setup.sh /usr/local/bin/init-wrapper.sh

RUN cp -a /home/lainos /etc/skel-lainos
CMD ["/usr/local/bin/init-wrapper.sh"]

ARG GO_VERSION=1.23

FROM golang:${GO_VERSION} AS go-builder
WORKDIR /build
COPY gateway/ .
RUN CGO_ENABLED=0 go build -o wh-gateway .

FROM golang:1.25 AS lain-builder
WORKDIR /build
COPY lain/ .
RUN CGO_ENABLED=0 go build -o lain .

FROM fedora:latest AS rpm-builder
RUN dnf install -y rpm-build rpmdevtools git nodejs npm curl && dnf clean all
RUN rpmdev-setuptree

COPY --from=go-builder /build/wh-gateway /tmp/sources/wh-gateway
COPY --from=lain-builder /build/lain /tmp/sources/lain
COPY systemd/wh-gateway.service /tmp/sources/wh-gateway.service
COPY systemd/camofox.service /tmp/sources/camofox.service

RUN CAMOFOX_VERSION=$(curl -sL https://api.github.com/repos/jo-inc/camofox-browser/releases/latest \
    | grep '"tag_name"' \
    | sed -E 's/.*"v([^"]+)".*/\1/') \
    && echo "${CAMOFOX_VERSION}" > /tmp/camofox-version \
    && git clone --branch "v${CAMOFOX_VERSION}" --depth 1 \
       https://github.com/jo-inc/camofox-browser.git /tmp/sources/camofox \
    && cd /tmp/sources/camofox \
    && npm install --production

COPY packaging/wh-gateway.spec /root/rpmbuild/SPECS/
COPY packaging/lain.spec /root/rpmbuild/SPECS/
COPY packaging/camofox.spec /root/rpmbuild/SPECS/

RUN cp /tmp/sources/wh-gateway /root/rpmbuild/SOURCES/ \
    && cp /tmp/sources/wh-gateway.service /root/rpmbuild/SOURCES/ \
    && cp /tmp/sources/lain /root/rpmbuild/SOURCES/ \
    && tar -czf /root/rpmbuild/SOURCES/camofox.tar.gz -C /tmp/sources camofox \
    && cp /tmp/sources/camofox.service /root/rpmbuild/SOURCES/

RUN WH_GATEWAY_VERSION=0.1.0 \
    rpmbuild -bb /root/rpmbuild/SPECS/wh-gateway.spec

RUN LAIN_VERSION=0.1.0 \
    rpmbuild -bb /root/rpmbuild/SPECS/lain.spec

RUN CAMOFOX_VERSION=$(cat /tmp/camofox-version) \
    rpmbuild -bb /root/rpmbuild/SPECS/camofox.spec

FROM ghcr.io/ublue-os/ucore-minimal:stable

COPY --from=rpm-builder /root/rpmbuild/RPMS/x86_64/*.rpm /tmp/rpms/

RUN mkdir -p /var/usrlocal/bin && \
    rpm-ostree install \
    nodejs \
    curl \
    git unzip python3 \
    gtk3 dbus-glib libXt alsa-lib libXcomposite libXcursor libXdamage libXfixes \
    libXi libXrandr libXrender libXScrnSaver libXtst \
    mesa-libEGL mesa-dri-drivers libgbm \
    xorg-x11-server-Xvfb \
    fontconfig google-noto-emoji-color-fonts liberation-fonts-common \
    https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-x86_64.rpm

RUN rpm-ostree install \
    /tmp/rpms/wh-gateway-*.rpm \
    /tmp/rpms/lain-*.rpm \
    /tmp/rpms/camofox-*.rpm \
    && rm -rf /tmp/rpms

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
    '  docs/           /usr/share/doc/lainos/' \
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

COPY docs/ /usr/share/doc/lainos/

COPY systemd/first-boot-setup.service /usr/lib/systemd/system/
COPY systemd/98-lainos.preset /usr/lib/systemd/system-preset/
COPY systemd/cloudflared.service /etc/systemd/user/

COPY scripts/first-boot-setup.sh /usr/local/bin/first-boot-setup.sh
COPY scripts/init-wrapper.sh /usr/local/bin/init-wrapper.sh
RUN chmod +x /usr/local/bin/first-boot-setup.sh /usr/local/bin/init-wrapper.sh

RUN cp -a /home/lainos /etc/skel-lainos

CMD ["/usr/local/bin/init-wrapper.sh"]

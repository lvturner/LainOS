#!/bin/bash
for unit in \
    proc-sys-fs-binfmt_misc.automount \
    sys-kernel-config.mount \
    sys-kernel-debug.mount \
    sys-kernel-tracing.mount \
    var-lib-nfs-rpc_pipefs.mount \
    audit-rules.service \
    auditd.service \
    bootloader-update.service \
    chronyd.service \
    coreos-populate-lvmdevices.service; do
    ln -sf /dev/null "/etc/systemd/system/$unit"
done

systemctl disable systemd-resolved.service 2>/dev/null || true

rm -f /etc/resolv.conf
cat > /etc/resolv.conf <<EOF
nameserver 1.1.1.1
nameserver 8.8.8.8
EOF
chmod 644 /etc/resolv.conf

exec /sbin/init

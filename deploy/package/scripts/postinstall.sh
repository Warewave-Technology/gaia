#!/bin/sh
# Creates the gaia account and gives it subordinate IDs for rootless
# podman (Ansible runs); the services are not started: their
# configuration in /etc/gaia comes first.
set -e
if command -v systemd-sysusers >/dev/null 2>&1; then
  systemd-sysusers /usr/lib/sysusers.d/gaia.conf
elif ! getent passwd gaia >/dev/null; then
  useradd --system --home-dir /var/lib/gaia --shell /usr/sbin/nologin --user-group gaia
fi
if ! grep -q '^gaia:' /etc/subuid 2>/dev/null; then
  echo 'gaia:200000:65536' >> /etc/subuid
fi
if ! grep -q '^gaia:' /etc/subgid 2>/dev/null; then
  echo 'gaia:200000:65536' >> /etc/subgid
fi
chgrp gaia /etc/gaia /etc/gaia/gaia.env /etc/gaia/gaia-ipam.env
chmod 0750 /etc/gaia
chmod 0640 /etc/gaia/gaia.env /etc/gaia/gaia-ipam.env
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
fi

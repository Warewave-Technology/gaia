#!/bin/sh
# On removal (not on upgrade) the services stop.
set -e
case "$1" in
  remove|0) ;;
  *) exit 0 ;;
esac
if [ -d /run/systemd/system ]; then
  systemctl stop gaia.service gaia-ipam.service >/dev/null 2>&1 || true
  systemctl disable gaia.service gaia-ipam.service >/dev/null 2>&1 || true
fi

#!/usr/bin/env bash
# Uninstall privatedns from a Linux host. Keeps /var/lib/privatedns and
# /etc/privatedns unless --purge is passed.
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo "must run as root"; exit 1; }

PURGE=0
[ "${1-}" = "--purge" ] && PURGE=1

systemctl stop    privatedns.service 2>/dev/null || true
systemctl disable privatedns.service 2>/dev/null || true
rm -f /etc/systemd/system/privatedns.service
systemctl daemon-reload

rm -f /usr/local/bin/privatedns

if [ "$PURGE" = 1 ]; then
  echo "purging data + config"
  rm -rf /var/lib/privatedns /etc/privatedns
  userdel privatedns 2>/dev/null || true
  groupdel privatedns 2>/dev/null || true
fi

echo "privatedns uninstalled."

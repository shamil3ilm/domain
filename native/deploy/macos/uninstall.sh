#!/usr/bin/env bash
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo "must run as root"; exit 1; }

PURGE=0
[ "${1-}" = "--purge" ] && PURGE=1

launchctl unload /Library/LaunchDaemons/com.privatedns.plist 2>/dev/null || true
rm -f /Library/LaunchDaemons/com.privatedns.plist
rm -f /usr/local/bin/privatedns /usr/local/bin/privatedns-launchd-wrapper

if [ "$PURGE" = 1 ]; then
  rm -rf /usr/local/var/privatedns /etc/privatedns
fi

echo "privatedns uninstalled."

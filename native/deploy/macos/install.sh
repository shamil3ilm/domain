#!/usr/bin/env bash
# privatedns macOS installer. Registers a system launchd daemon.
#
# Usage:
#   sudo ./install.sh [--binary <path>]

set -euo pipefail

log() { printf '[install] %s\n' "$*"; }
die() { printf '[install] %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo $0)"
[ "$(uname -s)" = "Darwin" ] || die "this script is for macOS"

BINARY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BINARY="$2"; shift 2 ;;
    -h|--help) grep '^# ' "$0" | sed 's/^# //'; exit 0 ;;
    *) die "unknown arg: $1" ;;
  esac
done

if [ -z "$BINARY" ]; then
  script_dir=$(cd "$(dirname "$0")" && pwd)
  for candidate in \
      "$script_dir/../../privatedns" \
      "$script_dir/../privatedns" \
      "$script_dir/privatedns" \
      "$(pwd)/privatedns"; do
    if [ -x "$candidate" ]; then BINARY="$candidate"; break; fi
  done
fi
[ -n "$BINARY" ] && [ -x "$BINARY" ] || die "cannot find privatedns binary (--binary <path>)"

log "installing binary to /usr/local/bin/privatedns"
install -m 0755 -o root -g wheel "$BINARY" /usr/local/bin/privatedns

install -d -m 0750 /usr/local/var/privatedns
install -d -m 0755 /usr/local/var/log

ENV_FILE=/etc/privatedns/privatedns.env
mkdir -p /etc/privatedns
if [ ! -f "$ENV_FILE" ]; then
  ADMIN_PW=$(openssl rand -base64 24 | tr -d '=/+')
  cat > "$ENV_FILE" <<EOF
PRIVATEDNS_DATA_DIR=/usr/local/var/privatedns
PRIVATEDNS_PRIVATE_TLD=myworld
PRIVATEDNS_DNS_ADDR=:53
PRIVATEDNS_API_ADDR=127.0.0.1:8080
PRIVATEDNS_ADMIN_EMAIL=admin@local
PRIVATEDNS_ADMIN_PASSWORD=${ADMIN_PW}
PRIVATEDNS_ALLOW_QUERY_FROM=
PRIVATEDNS_ALLOW_RECURSION_FROM=
PRIVATEDNS_UPSTREAMS=1.1.1.1:53,9.9.9.9:53
PRIVATEDNS_LOG_LEVEL=info
EOF
  chmod 0640 "$ENV_FILE"
  BOOTSTRAP_PW="$ADMIN_PW"
else
  log "$ENV_FILE exists — keeping current values"
  BOOTSTRAP_PW=""
fi

# launchd doesn't have an EnvironmentFile concept — we source the env at
# start via a wrapper.
WRAPPER=/usr/local/bin/privatedns-launchd-wrapper
cat > "$WRAPPER" <<'EOF'
#!/bin/sh
set -eu
if [ -f /etc/privatedns/privatedns.env ]; then
  set -a
  . /etc/privatedns/privatedns.env
  set +a
fi
exec /usr/local/bin/privatedns
EOF
chmod 0755 "$WRAPPER"

PLIST_SRC="$(cd "$(dirname "$0")" && pwd)/com.privatedns.plist"
PLIST_DST=/Library/LaunchDaemons/com.privatedns.plist

# Rewrite the plist to use the wrapper.
python3 - <<PY
import plistlib
with open("$PLIST_SRC","rb") as f:
    p = plistlib.load(f)
p["ProgramArguments"] = ["$WRAPPER"]
with open("$PLIST_DST","wb") as f:
    plistlib.dump(p, f)
PY
chown root:wheel "$PLIST_DST"
chmod 0644 "$PLIST_DST"

log "loading launchd unit"
launchctl unload "$PLIST_DST" 2>/dev/null || true
launchctl load -w "$PLIST_DST"

log "done."
if [ -n "$BOOTSTRAP_PW" ]; then
  cat <<EOF

========================================
 privatedns bootstrap credentials
   email:    admin@local
   password: ${BOOTSTRAP_PW}
 (saved in $ENV_FILE)
========================================
EOF
fi

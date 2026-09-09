#!/usr/bin/env bash
# privatedns Linux installer.
#
# Usage:
#   sudo ./install.sh [--binary <path>] [--tld myworld] [--allow-recursion <cidr,cidr>]
#
# What it does:
#   1. Creates a system user `privatedns`.
#   2. Copies the binary to /usr/local/bin/privatedns (or downloads one — TODO).
#   3. Creates /etc/privatedns/privatedns.env with sensible defaults.
#   4. Installs the systemd unit at /etc/systemd/system/privatedns.service.
#   5. Enables + starts the service.
#   6. Prints the admin credentials once.
#
# Idempotent: safe to re-run. Existing env file / data / user are preserved.

set -euo pipefail

RED=$'\e[31m'; GRN=$'\e[32m'; YLW=$'\e[33m'; RST=$'\e[0m'
log()  { printf '%s[install]%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '%s[install]%s %s\n' "$YLW" "$RST" "$*" >&2; }
die()  { printf '%s[install]%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo $0)"
command -v systemctl >/dev/null || die "systemd required"

# ---- Defaults ---------------------------------------------------------------
BINARY=""
TLD="myworld"
ALLOW_QUERY=""
ALLOW_RECURSION=""       # empty = loopback only, safe default
DNS_ADDR=":53"
API_ADDR="127.0.0.1:8080"

# ---- Args -------------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --binary)          BINARY="$2"; shift 2 ;;
    --tld)             TLD="$2"; shift 2 ;;
    --allow-query)     ALLOW_QUERY="$2"; shift 2 ;;
    --allow-recursion) ALLOW_RECURSION="$2"; shift 2 ;;
    --dns-addr)        DNS_ADDR="$2"; shift 2 ;;
    --api-addr)        API_ADDR="$2"; shift 2 ;;
    -h|--help)
      grep '^# ' "$0" | sed 's/^# //'; exit 0 ;;
    *) die "unknown arg: $1" ;;
  esac
done

# Auto-detect the binary if not specified.
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

# ---- User -------------------------------------------------------------------
if ! getent passwd privatedns >/dev/null; then
  log "creating system user 'privatedns'"
  useradd --system --no-create-home --shell /usr/sbin/nologin --user-group privatedns
else
  log "user 'privatedns' already exists"
fi

# ---- Binary -----------------------------------------------------------------
log "installing binary to /usr/local/bin/privatedns"
install -m 0755 -o root -g root "$BINARY" /usr/local/bin/privatedns

# ---- Directories ------------------------------------------------------------
install -d -m 0750 -o privatedns -g privatedns /var/lib/privatedns
install -d -m 0750 -o root       -g privatedns /etc/privatedns

# ---- Env file (only create if missing) --------------------------------------
ENV_FILE=/etc/privatedns/privatedns.env
if [ ! -f "$ENV_FILE" ]; then
  ADMIN_PW=$(openssl rand -base64 24 | tr -d '=/+')
  log "generating $ENV_FILE with fresh admin credentials"
  umask 077
  cat > "$ENV_FILE" <<EOF
# privatedns runtime config. Edit and: systemctl restart privatedns

PRIVATEDNS_DATA_DIR=/var/lib/privatedns
PRIVATEDNS_PRIVATE_TLD=${TLD}
PRIVATEDNS_DNS_ADDR=${DNS_ADDR}
PRIVATEDNS_API_ADDR=${API_ADDR}

# Admin (used on first boot only; change from the dashboard afterwards).
PRIVATEDNS_ADMIN_EMAIL=admin@local
PRIVATEDNS_ADMIN_PASSWORD=${ADMIN_PW}

# ACLs.
#   ALLOW_QUERY_FROM     — everyone can query if empty (safe for authoritative).
#   ALLOW_RECURSION_FROM — loopback only if empty (safe default, refuses open recursion).
PRIVATEDNS_ALLOW_QUERY_FROM=${ALLOW_QUERY}
PRIVATEDNS_ALLOW_RECURSION_FROM=${ALLOW_RECURSION}

# Upstream resolvers used by the recursor (comma-separated).
PRIVATEDNS_UPSTREAMS=1.1.1.1:53,9.9.9.9:53

PRIVATEDNS_LOG_LEVEL=info
EOF
  chown root:privatedns "$ENV_FILE"
  chmod 0640 "$ENV_FILE"
  BOOTSTRAP_PW="$ADMIN_PW"
else
  log "$ENV_FILE exists — keeping current values"
  BOOTSTRAP_PW=""
fi

# ---- Systemd unit -----------------------------------------------------------
UNIT_SRC="$(cd "$(dirname "$0")" && pwd)/privatedns.service"
UNIT_DST=/etc/systemd/system/privatedns.service
if [ ! -f "$UNIT_SRC" ]; then
  die "cannot find privatedns.service alongside this script (looked at $UNIT_SRC)"
fi
log "installing systemd unit"
install -m 0644 -o root -g root "$UNIT_SRC" "$UNIT_DST"

# ---- Port 53 conflict warning ----------------------------------------------
if ss -lun 2>/dev/null | grep -qE ':53\b' || ss -ltn 2>/dev/null | grep -qE ':53\b'; then
  warn "something else is listening on port 53. On Ubuntu that's usually"
  warn "systemd-resolved — disable its stub with:"
  warn "  sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf"
  warn "  systemctl restart systemd-resolved"
  warn "…and then rerun: systemctl start privatedns"
fi

# ---- Enable + start ---------------------------------------------------------
systemctl daemon-reload
systemctl enable privatedns.service >/dev/null
if ! systemctl restart privatedns.service; then
  warn "service failed to start — check: journalctl -u privatedns --no-pager -n 50"
  exit 1
fi

log "service is up:"
systemctl --no-pager --lines=0 status privatedns.service || true

if [ -n "$BOOTSTRAP_PW" ]; then
  cat <<EOF

========================================
 privatedns bootstrap credentials
   email:    admin@local
   password: ${BOOTSTRAP_PW}
 (also saved in ${ENV_FILE})
========================================
Point your DNS at this host:
  ${DNS_ADDR}       (bind port 53 — configure firewall)
Reach the dashboard on:
  http://<host>${API_ADDR#127.0.0.1}/     (via SSH tunnel or bind API to 0.0.0.0)

Next steps:
  * dig @<host> www.example.myworld A     (empty result — no records yet)
  * create records via API or dashboard
  * add firewall rules limiting who can query the DNS
EOF
fi

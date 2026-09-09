#!/usr/bin/env bash
# install-with-mail.sh — install privatedns AND mail-service on the same
# Debian/Ubuntu host, wire the DNS bridge between them, and expose a single
# `sili.target` that manages both as a unit.
#
# Usage:
#   sudo ./install-with-mail.sh --mail-binary <path> --domain <domain> \
#        [--admin-email admin@local]
#
# Assumptions verified against mail-service upstream at commit-time:
#   * mail-service ships deploy/systemd/mailservice.service
#   * mail-service binary is a single static Go executable installed to
#     /usr/local/bin/mailservice
#   * mail-service reads /etc/mailservice/env and uses /var/lib/mailservice
#     for state
#   * mail-service ports: HTTP 8035, admin 8036, SMTP 25 (cloud), SUB 587
#   * mail-service DNS bridge honors MAIL_DNS_PUBLISHER=privatedns +
#     MAIL_DNS_PUBLISHER_URL/USER/TOKEN
#   * mail-service does NOT ship its own install.sh — this script installs it
#
# If any of these change upstream, adjust the constants near the top of this
# script and re-run — everything except SERVICE_NAME/USER is easy to override.
#
# Idempotency contract: safe to re-run. Preserves existing valid env files,
# never rotates a working DNS bridge token, adds firewall rules once, and
# won't fail merely because privatedns is already installed.

set -euo pipefail

# ---- constants --------------------------------------------------------------
readonly PRIVATEDNS_UNIT=/etc/systemd/system/privatedns.service
readonly PRIVATEDNS_ENV=/etc/privatedns/privatedns.env
readonly PRIVATEDNS_HEALTH_URL="http://127.0.0.1:8080/healthz"

readonly MAIL_UNIT=/etc/systemd/system/mailservice.service
readonly MAIL_ENV=/etc/mailservice/env
readonly MAIL_STATE_DIR=/var/lib/mailservice
readonly MAIL_UPSTREAM_UNIT_URL="https://raw.githubusercontent.com/shamil3ilm/mail-service/main/deploy/systemd/mailservice.service"

readonly SILI_TARGET=/etc/systemd/system/sili.target

# ---- ANSI + logging ---------------------------------------------------------
if [ -t 1 ]; then
  RED=$'\e[31m'; GRN=$'\e[32m'; YLW=$'\e[33m'; RST=$'\e[0m'
else
  RED=""; GRN=""; YLW=""; RST=""
fi
log()  { printf '%s[install]%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '%s[install]%s %s\n' "$YLW" "$RST" "$*" >&2; }
die()  { printf '%s[install]%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

# ---- args -------------------------------------------------------------------
MAIL_BINARY=""
DOMAIN=""
ADMIN_EMAIL="admin@local"

usage() {
  cat <<EOF
Usage: sudo $0 --mail-binary <path> --domain <domain> [--admin-email <email>]

  --mail-binary   Path to the mail-service binary (built from
                  github.com/shamil3ilm/mail-service).
  --domain        Mail domain to prime (e.g. mail.example.com).
                  A privatedns zone with this name will be created if
                  it doesn't already exist.
  --admin-email   privatedns admin email (default: admin@local).

Reruns are safe.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --mail-binary) MAIL_BINARY="$2"; shift 2 ;;
    --domain)      DOMAIN="$2"; shift 2 ;;
    --admin-email) ADMIN_EMAIL="$2"; shift 2 ;;
    -h|--help)     usage; exit 0 ;;
    *) die "unknown arg: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo $0 ...)"
[ -n "$MAIL_BINARY" ] || { usage; die "--mail-binary is required"; }
[ -f "$MAIL_BINARY" ] && [ -x "$MAIL_BINARY" ] || die "not an executable: $MAIL_BINARY"
[ -n "$DOMAIN" ] || { usage; die "--domain is required"; }

command -v systemctl >/dev/null || die "systemd required"
command -v curl      >/dev/null || die "curl required"

# --------------------------------------------------------------------------
# 1. privatedns
# --------------------------------------------------------------------------

log "step 1/6: privatedns"

script_dir=$(cd "$(dirname "$0")" && pwd)
if [ ! -x "$PRIVATEDNS_UNIT" ] || ! systemctl is-enabled privatedns >/dev/null 2>&1; then
  log "  running privatedns installer"
  "$script_dir/install.sh" \
    --tld "$(echo "$DOMAIN" | rev | cut -d. -f1-2 | rev)"
else
  log "  privatedns already installed — skipping install.sh"
fi

# --------------------------------------------------------------------------
# 2. Wait for privatedns to be healthy
# --------------------------------------------------------------------------

log "step 2/6: waiting for privatedns /healthz"
deadline=$((SECONDS + 60))
while [ $SECONDS -lt $deadline ]; do
  if curl -fsS --max-time 2 "$PRIVATEDNS_HEALTH_URL" >/dev/null 2>&1; then
    log "  ready"
    break
  fi
  sleep 1
done
if ! curl -fsS --max-time 2 "$PRIVATEDNS_HEALTH_URL" >/dev/null 2>&1; then
  die "privatedns didn't respond at $PRIVATEDNS_HEALTH_URL within 60s. Check: journalctl -u privatedns"
fi

# --------------------------------------------------------------------------
# 3. Get/create a bridge credential for mail-service
# --------------------------------------------------------------------------

log "step 3/6: bridge credential"

# Prefer an API key over the admin password — one-click revocation. We check
# if a key with a marker name already exists on the local mail-service env
# file; if so, keep it. Otherwise mint a new one.
BRIDGE_TOKEN=""
if [ -f "$MAIL_ENV" ] && grep -q '^MAIL_DNS_PUBLISHER_TOKEN=' "$MAIL_ENV"; then
  existing=$(grep '^MAIL_DNS_PUBLISHER_TOKEN=' "$MAIL_ENV" | cut -d= -f2-)
  if [ -n "$existing" ]; then
    log "  reusing existing bridge token from $MAIL_ENV"
    BRIDGE_TOKEN="$existing"
  fi
fi

if [ -z "$BRIDGE_TOKEN" ]; then
  log "  minting a fresh API key via privatedns login"
  # Log in as admin to obtain a JWT, then create an API key.
  admin_pw=$(grep '^PRIVATEDNS_ADMIN_PASSWORD=' "$PRIVATEDNS_ENV" | cut -d= -f2-)
  admin_email=$(grep '^PRIVATEDNS_ADMIN_EMAIL='    "$PRIVATEDNS_ENV" | cut -d= -f2-)
  [ -n "$admin_pw" ] || die "cannot read admin password from $PRIVATEDNS_ENV"

  # Login
  jwt=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/auth/login \
    -H 'content-type: application/json' \
    -d "{\"email\":\"$admin_email\",\"password\":\"$admin_pw\"}" \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [ -n "$jwt" ] || die "login to privatedns failed"

  # Create key
  BRIDGE_TOKEN=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/keys \
    -H "authorization: bearer $jwt" -H 'content-type: application/json' \
    -d '{"name":"mail-service-bridge"}' \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [ -n "$BRIDGE_TOKEN" ] || die "could not mint API key"
  log "  minted key (redacted); stored only in $MAIL_ENV"
fi

# --------------------------------------------------------------------------
# 4. Install mail-service
# --------------------------------------------------------------------------

log "step 4/6: mail-service"

# 4a. Binary
install -m 0755 -o root -g root "$MAIL_BINARY" /usr/local/bin/mailservice

# 4b. User + dirs
if ! getent passwd mailservice >/dev/null; then
  useradd --system --no-create-home --shell /usr/sbin/nologin --user-group mailservice
fi
install -d -m 0750 -o mailservice -g mailservice "$MAIL_STATE_DIR"
install -d -m 0750 -o root        -g mailservice /etc/mailservice

# 4c. Env file — only overwrite if we lack a valid one. Otherwise update the
# bridge token line in place, leaving user-set values alone.
if [ ! -f "$MAIL_ENV" ]; then
  umask 077
  cat > "$MAIL_ENV" <<EOF
# Managed by install-with-mail.sh. Edit and: systemctl restart mailservice
MAIL_MODE=cloud
MAIL_LISTEN_ADDR=0.0.0.0
MAIL_LOG_LEVEL=info
MAIL_LOG_FORMAT=json

MAIL_HTTP_PORT=8035
MAIL_ADMIN_PORT=8036
MAIL_SMTP_PORT=25
MAIL_SUBMISSION_PORT=587

MAIL_DB_PATH=$MAIL_STATE_DIR/mail.db
MAIL_RAW_STORE_PATH=$MAIL_STATE_DIR/raw

MAIL_AUTO_VERIFY_DOMAINS=.test,.local,.localhost
MAIL_DEFAULT_MAILBOX_MODE=capture
MAIL_RELAY_PROVIDER=none

# DNS bridge → local privatedns
MAIL_DNS_PUBLISHER=privatedns
MAIL_DNS_PUBLISHER_URL=http://127.0.0.1:8080
MAIL_DNS_PUBLISHER_USER=$ADMIN_EMAIL
MAIL_DNS_PUBLISHER_TOKEN=$BRIDGE_TOKEN

MAIL_SHUTDOWN_TIMEOUT=15s
EOF
  chown root:mailservice "$MAIL_ENV"
  chmod 0640 "$MAIL_ENV"
  log "  wrote $MAIL_ENV"
else
  # Idempotent update: replace or add the DNS bridge lines, leave everything else.
  set_env() {
    local key="$1" val="$2"
    if grep -q "^${key}=" "$MAIL_ENV"; then
      # Use `|` as sed delimiter — token may contain slashes.
      sed -i "s|^${key}=.*|${key}=${val}|" "$MAIL_ENV"
    else
      printf '%s=%s\n' "$key" "$val" >> "$MAIL_ENV"
    fi
  }
  set_env MAIL_DNS_PUBLISHER       privatedns
  set_env MAIL_DNS_PUBLISHER_URL   http://127.0.0.1:8080
  set_env MAIL_DNS_PUBLISHER_USER  "$ADMIN_EMAIL"
  set_env MAIL_DNS_PUBLISHER_TOKEN "$BRIDGE_TOKEN"
  chown root:mailservice "$MAIL_ENV"
  chmod 0640 "$MAIL_ENV"
  log "  updated bridge config in $MAIL_ENV (other keys preserved)"
fi

# 4d. Systemd unit — fetch upstream if we don't already have it.
if [ ! -f "$MAIL_UNIT" ]; then
  log "  fetching mailservice.service unit from upstream"
  curl -fsS -o "$MAIL_UNIT" "$MAIL_UPSTREAM_UNIT_URL" \
    || die "could not fetch $MAIL_UPSTREAM_UNIT_URL — check network or supply manually"
  chown root:root "$MAIL_UNIT"
  chmod 0644 "$MAIL_UNIT"
fi

systemctl daemon-reload
systemctl enable mailservice >/dev/null
systemctl restart mailservice
log "  mailservice running"

# --------------------------------------------------------------------------
# 5. sili.target — bring both services up as a unit
# --------------------------------------------------------------------------

log "step 5/6: sili.target"
install -m 0644 -o root -g root "$script_dir/../systemd/sili.target" "$SILI_TARGET"
systemctl daemon-reload
systemctl enable sili.target >/dev/null
log "  sili.target enabled — 'systemctl start sili.target' brings up both services"

# --------------------------------------------------------------------------
# 6. Firewall — idempotent UFW rules if ufw is present + already active
# --------------------------------------------------------------------------

log "step 6/6: firewall"
if command -v ufw >/dev/null; then
  # Don't enable UFW here — enabling on a fresh SSH session locks the admin
  # out. We only add rules if UFW is already enabled by the operator.
  if ufw status 2>/dev/null | grep -q '^Status: active'; then
    for rule in "22/tcp" "25/tcp" "53/tcp" "53/udp" "587/tcp" "443/tcp"; do
      # UFW dedupes silently, but grep is a cheaper visible signal.
      if ! ufw status | grep -q " ${rule%%/*}/${rule##*/}"; then
        ufw allow "$rule" >/dev/null
      fi
    done
    log "  UFW rules verified (22, 25, 53, 587, 443)"
  else
    warn "  UFW is installed but not enabled. Not enabling automatically —"
    warn "  doing so may lock you out via SSH. When ready, run:"
    warn "    sudo ufw allow ssh && sudo ufw allow 25/tcp && sudo ufw allow 53/tcp"
    warn "    sudo ufw allow 53/udp && sudo ufw allow 587/tcp && sudo ufw allow 443/tcp"
    warn "    sudo ufw enable"
  fi
else
  warn "  ufw not installed. Configure your firewall to allow: 22, 25, 53, 587, 443."
fi

# --------------------------------------------------------------------------
# DNS output — what the admin has to configure at the registrar
# --------------------------------------------------------------------------

# Best-effort public IP.
public_ip=$(curl -fsS --max-time 3 https://api.ipify.org 2>/dev/null || echo "<VPS-PUBLIC-IP>")
public_ip6=$(curl -fsS --max-time 3 https://api6.ipify.org 2>/dev/null || echo "")

# Path 2: NS host lives in the *parent* of the delegated zone, so glue and
# recursion work. For "mail.example.com" the parent is "example.com". For a
# 2-label domain like "example.com" there is no useful parent, so we keep
# the NS inside the zone itself and require the registrar to accept in-zone
# glue.
label_count=$(echo "$DOMAIN" | awk -F. '{print NF}')
if [ "$label_count" -le 2 ]; then
  parent_domain="$DOMAIN"
else
  parent_domain=$(echo "$DOMAIN" | cut -d. -f2-)
fi
ns_host="ns1.${parent_domain}"

cat <<EOF

================================================================================
Installation complete.

managed by privatedns (already applied on this box):
  zone ${DOMAIN}
  records will populate as mail-service creates the mail domain.

>>> YOU MUST configure the following at your DOMAIN REGISTRAR / parent DNS <<<

  ${ns_host}   IN A     ${public_ip}$([ -n "$public_ip6" ] && printf '\n  %s   IN AAAA  %s' "${ns_host}" "$public_ip6")
  ${DOMAIN}    IN NS    ${ns_host}.

If your registrar requires a **glue record** to be registered at the parent
zone (many do — look for a "child nameservers" or "glue records" panel):

  ${ns_host}   glue A  ${public_ip}

Once propagated (verify with 'dig +trace ${DOMAIN} NS'), the mail-service
DNS bridge will populate SPF/DKIM/DMARC/MX records inside ${DOMAIN}
automatically. Until then, you can seed them manually via the
/records:batch API — see docs/integration-mail.md.

Check services with:
  systemctl status privatedns mailservice
  systemctl start sili.target      # brings up both

Health:
  curl -sS http://127.0.0.1:8080/healthz     # privatedns
  curl -sS http://127.0.0.1:8035/healthz     # mail-service (once its HTTP is up)
================================================================================
EOF

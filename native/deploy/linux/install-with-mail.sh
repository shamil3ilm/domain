#!/usr/bin/env bash
# install-with-mail.sh — install privatedns AND mail-service on the same
# Debian/Ubuntu host, wire the DNS bridge between them, and expose a single
# `sili.target` that manages both as a unit.
#
# Usage:
#   sudo ./install-with-mail.sh --mail-binary <path> --domain <domain> \
#        [--mail-installer <path>] [--admin-email admin@local]
#
# Contract verified against the mail-service repo at commit-time:
#   * mail-service ships deploy/install.sh that accepts `--local-binary <path>`
#     and handles: binary install to /usr/local/bin/mailservice, mailservice
#     system user, /etc/mailservice/env template, /var/lib/mailservice state
#     dir, systemd unit at /etc/systemd/system/mailservice.service, and UFW
#     rules for 25/587/443. We delegate all of that to it.
#   * mail-service reads MAIL_DNS_PUBLISHER=privatedns +
#     MAIL_DNS_PUBLISHER_URL/USER/TOKEN from /etc/mailservice/env
#     (internal/config/config.go). We only append/update those keys.
#
# What THIS script owns:
#   * running privatedns's own installer (if not already installed)
#   * minting a dedicated API key for the mail-service bridge
#   * patching the DNS bridge env vars into /etc/mailservice/env
#   * installing sili.target
#   * adding UFW rule for 53 (privatedns's port — mail install.sh only opens
#     25/587/443)
#   * printing the registrar delegation checklist
#
# Idempotency contract: safe to re-run. Preserves existing valid env files,
# never rotates a working DNS bridge token, adds firewall rules once, and
# won't fail merely because privatedns or mail-service is already installed.

set -euo pipefail

# ---- constants --------------------------------------------------------------
readonly PRIVATEDNS_UNIT=/etc/systemd/system/privatedns.service
readonly PRIVATEDNS_ENV=/etc/privatedns/privatedns.env
readonly PRIVATEDNS_HEALTH_URL="http://127.0.0.1:8080/healthz"

readonly MAIL_ENV=/etc/mailservice/env

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
MAIL_INSTALLER=""
DOMAIN=""
ADMIN_EMAIL="admin@local"

usage() {
  cat <<EOF
Usage: sudo $0 --mail-binary <path> --domain <domain>
              [--mail-installer <path>] [--admin-email <email>]

  --mail-binary     Path to the mail-service binary (built from
                    github.com/shamil3ilm/mail-service).
  --domain          Mail domain to prime (e.g. mail.example.com).
                    A privatedns zone with this name will be created if
                    it doesn't already exist.
  --mail-installer  Path to mail-service's deploy/install.sh. If omitted,
                    we look for it at <mail-binary>/../deploy/install.sh
                    (i.e. the standard layout of a mail-service checkout).
  --admin-email     privatedns admin email (default: admin@local).

Reruns are safe.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --mail-binary)    MAIL_BINARY="$2"; shift 2 ;;
    --mail-installer) MAIL_INSTALLER="$2"; shift 2 ;;
    --domain)         DOMAIN="$2"; shift 2 ;;
    --admin-email)    ADMIN_EMAIL="$2"; shift 2 ;;
    -h|--help)        usage; exit 0 ;;
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
if [ ! -f "$PRIVATEDNS_UNIT" ] || ! systemctl is-enabled privatedns >/dev/null 2>&1; then
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
# 3. Bridge credential
# --------------------------------------------------------------------------
#
# Prefer an API key over the admin password: one-click revocation and it
# doesn't disrupt the operator's login when rotated. This uses privatedns's
# existing POST /api/v1/keys endpoint (see internal/httpapi/server.go).

log "step 3/6: bridge credential"

BRIDGE_TOKEN=""
if [ -f "$MAIL_ENV" ] && grep -q '^MAIL_DNS_PUBLISHER_TOKEN=' "$MAIL_ENV"; then
  existing=$(grep '^MAIL_DNS_PUBLISHER_TOKEN=' "$MAIL_ENV" | cut -d= -f2-)
  if [ -n "$existing" ]; then
    log "  reusing existing bridge token from $MAIL_ENV"
    BRIDGE_TOKEN="$existing"
  fi
fi

if [ -z "$BRIDGE_TOKEN" ]; then
  log "  minting a fresh API key via privatedns"
  admin_pw=$(grep '^PRIVATEDNS_ADMIN_PASSWORD=' "$PRIVATEDNS_ENV" | cut -d= -f2-)
  admin_email=$(grep '^PRIVATEDNS_ADMIN_EMAIL='   "$PRIVATEDNS_ENV" | cut -d= -f2- || true)
  [ -n "$admin_email" ] || admin_email="$ADMIN_EMAIL"
  [ -n "$admin_pw" ] || die "cannot read admin password from $PRIVATEDNS_ENV"

  # Login. Body pushed via stdin so the password never appears in argv.
  login_body=$(printf '{"email":"%s","password":"%s"}' "$admin_email" "$admin_pw")
  jwt=$(printf '%s' "$login_body" | curl -fsS -X POST \
    http://127.0.0.1:8080/api/v1/auth/login \
    -H 'content-type: application/json' \
    --data-binary @- \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [ -n "$jwt" ] || die "login to privatedns failed"

  # Create dedicated API key for the bridge.
  BRIDGE_TOKEN=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/keys \
    -H "authorization: bearer $jwt" -H 'content-type: application/json' \
    -d '{"name":"mail-service-bridge"}' \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  [ -n "$BRIDGE_TOKEN" ] || die "could not mint API key at POST /api/v1/keys"
  log "  minted key (redacted); stored only in $MAIL_ENV"
fi

# --------------------------------------------------------------------------
# 4. mail-service — delegate to its own installer
# --------------------------------------------------------------------------

log "step 4/6: mail-service (delegating to its installer)"

# Locate the mail-service installer. We refuse to guess: either the operator
# passes --mail-installer, or we look at the canonical location inside a
# mail-service checkout (deploy/install.sh sibling to bin/mailservice). If
# neither works, we stop — inventing a new interface silently is exactly
# what the joint-installer spec forbids.
if [ -z "$MAIL_INSTALLER" ]; then
  candidate="$(cd "$(dirname "$MAIL_BINARY")/.." 2>/dev/null && pwd)/deploy/install.sh"
  if [ -f "$candidate" ]; then
    MAIL_INSTALLER="$candidate"
    log "  found mail-service installer alongside binary: $MAIL_INSTALLER"
  fi
fi
if [ -z "$MAIL_INSTALLER" ]; then
  die "cannot locate mail-service's deploy/install.sh. Clone the mail-service
     repo so the binary and installer are colocated, or pass explicitly:
       --mail-installer /path/to/mail-service/deploy/install.sh"
fi
[ -f "$MAIL_INSTALLER" ] && [ -x "$MAIL_INSTALLER" ] || die "not an executable installer: $MAIL_INSTALLER"

# mail-service install.sh contract: --local-binary <path> installs binary,
# system user, env template, state dir, systemd unit, and UFW rules for
# 25/587/443. Idempotent per its own comment.
log "  running: $MAIL_INSTALLER --local-binary $MAIL_BINARY"
"$MAIL_INSTALLER" --local-binary "$MAIL_BINARY"

# Patch DNS bridge env vars into the env file the mail installer wrote.
# Preserve everything else — the operator will edit relay creds and cloud
# hostname independently.
[ -f "$MAIL_ENV" ] || die "$MAIL_ENV not created by mail-service installer"

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
log "  patched DNS bridge config into $MAIL_ENV (other keys preserved)"

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
# 6. Firewall — add DNS (53) on top of what mail install.sh opened
# --------------------------------------------------------------------------
#
# mail-service's installer already handles 25/587/443. We only add 53
# (tcp+udp) here — the DNS listener. Never enable UFW ourselves; doing so
# on a fresh SSH session locks the admin out.

log "step 6/6: firewall (DNS port 53)"
if command -v ufw >/dev/null; then
  if ufw status 2>/dev/null | grep -q '^Status: active'; then
    for rule in "53/tcp" "53/udp"; do
      if ! ufw status | grep -q " ${rule}"; then
        ufw allow "$rule" >/dev/null
      fi
    done
    log "  UFW rules verified (53/tcp, 53/udp)"
  else
    warn "  UFW installed but not enabled. When you enable it, also allow:"
    warn "    sudo ufw allow 53/tcp && sudo ufw allow 53/udp"
  fi
else
  warn "  ufw not installed. Configure your firewall to allow DNS: 53/tcp, 53/udp."
  warn "  (mail-service's installer already documented 25/587/443.)"
fi

# --------------------------------------------------------------------------
# DNS output — what the admin has to configure at the registrar
# --------------------------------------------------------------------------

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
================================================================================
EOF

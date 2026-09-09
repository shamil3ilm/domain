#!/usr/bin/env bash
# install.sh — bootstrap fresh installation.
# Idempotent: safe to re-run.
set -euo pipefail
cd "$(dirname "$0")/.."

RED=$'\e[31m'; GRN=$'\e[32m'; YLW=$'\e[33m'; RST=$'\e[0m'

log() { printf '%s[install]%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '%s[install]%s %s\n' "$YLW" "$RST" "$*" >&2; }
die() { printf '%s[install]%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

command -v docker >/dev/null || die "docker not installed"
docker compose version >/dev/null 2>&1 || die "docker compose plugin required"
command -v openssl >/dev/null || die "openssl not installed (needed to generate secrets)"

# ---- .env ------------------------------------------------------------------
if [ ! -f .env ]; then
  log "generating .env from .env.example with fresh secrets"
  cp .env.example .env

  gen_secret() { openssl rand -base64 32 | tr -d '\n'; }
  gen_hex()    { openssl rand -hex 32; }
  gen_pw()     { openssl rand -base64 24 | tr -d '\n/+='; }

  sed_i() {
    if [ "$(uname -s)" = "Darwin" ]; then sed -i '' "$@"; else sed -i "$@"; fi
  }

  sed_i "s|POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(gen_secret)|" .env
  sed_i "s|PDNS_API_KEY=.*|PDNS_API_KEY=$(gen_hex)|" .env
  sed_i "s|API_JWT_SECRET=.*|API_JWT_SECRET=$(openssl rand -base64 64 | tr -d '\n')|" .env
  sed_i "s|CA_PASSWORD=.*|CA_PASSWORD=$(gen_secret)|" .env
  sed_i "s|ADMIN_PASSWORD=.*|ADMIN_PASSWORD=$(gen_pw)|" .env
  sed_i "s|GRAFANA_ADMIN_PASSWORD=.*|GRAFANA_ADMIN_PASSWORD=$(gen_pw)|" .env

  log "wrote .env — review it and adjust PRIVATE_TLD / DNS_PUBLIC_IP / RECURSOR_ACL as needed"
else
  log ".env exists — keeping it. Delete it if you want a clean install."
fi

# ---- Build + start ---------------------------------------------------------
log "building images (this can take several minutes on first run)"
docker compose build

log "starting stack"
docker compose up -d

log "waiting for services to become healthy"
for i in $(seq 1 60); do
  unhealthy=$(docker compose ps --format json 2>/dev/null | grep -c '"Health":"unhealthy"' || true)
  starting=$(docker compose ps --format json 2>/dev/null | grep -c '"Health":"starting"' || true)
  if [ "$unhealthy" = "0" ] && [ "$starting" = "0" ]; then
    log "all services healthy"
    break
  fi
  sleep 2
done

# ---- Report bootstrap credentials ------------------------------------------
log "installation complete."
echo
if grep -q '^ADMIN_EMAIL=' .env && grep -q '^ADMIN_PASSWORD=' .env; then
  email=$(grep '^ADMIN_EMAIL='    .env | cut -d= -f2-)
  pw=$(grep    '^ADMIN_PASSWORD=' .env | cut -d= -f2-)
  echo "  Dashboard: https://$(grep '^DASHBOARD_DOMAIN=' .env | cut -d= -f2-)/"
  echo "  Admin:     $email"
  echo "  Password:  $pw"
  echo
  echo "  Add this line to /etc/hosts (or point your DNS at $(grep '^DNS_PUBLIC_IP=' .env | cut -d= -f2-)):"
  echo "     $(grep '^DNS_PUBLIC_IP=' .env | cut -d= -f2-)  $(grep '^DASHBOARD_DOMAIN=' .env | cut -d= -f2-)"
fi

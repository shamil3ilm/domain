#!/usr/bin/env bash
# backup.sh — snapshot everything into ./backups/<timestamp>.tar.gz
set -euo pipefail
cd "$(dirname "$0")/.."

TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="backups/${TS}"
mkdir -p "$OUT"

log() { echo "[backup] $*"; }

# ---- 1. Postgres dump (both schemas) ---------------------------------------
log "dumping postgres..."
docker compose exec -T postgres \
  pg_dump -U "${POSTGRES_USER:-privatedns}" -d "${POSTGRES_DB:-privatedns}" -Fc \
  > "$OUT/postgres.dump"

# ---- 2. CA data (root + intermediate keys, ca.json) ------------------------
log "archiving CA data..."
docker run --rm -v privatedns_ca_data:/data alpine \
  sh -c 'cd /data && tar czf - .' > "$OUT/ca_data.tar.gz"

# ---- 3. PowerDNS DNSSEC keys / crypto (they're in postgres, redundant with dump)
# ---- 4. Caddy data (issued certs) ------------------------------------------
log "archiving Caddy state..."
docker run --rm -v privatedns_caddy_data:/data alpine \
  sh -c 'cd /data && tar czf - .' > "$OUT/caddy_data.tar.gz"

# ---- 5. Environment + compose file ------------------------------------------
cp -f .env "$OUT/.env"
cp -f docker-compose.yml "$OUT/docker-compose.yml"

# ---- Wrap up ---------------------------------------------------------------
TARBALL="backups/privatedns-${TS}.tar.gz"
tar -C backups -czf "$TARBALL" "$TS"
rm -rf "$OUT"
sha256sum "$TARBALL" > "$TARBALL.sha256"
log "wrote $TARBALL"

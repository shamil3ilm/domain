#!/usr/bin/env bash
# restore.sh <backup.tar.gz> — restore from a backup created by backup.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

log() { echo "[restore] $*"; }
die() { echo "[restore] $*" >&2; exit 1; }

[ $# -eq 1 ] || die "usage: $0 <backup.tar.gz>"
ARCHIVE="$1"
[ -f "$ARCHIVE" ] || die "not found: $ARCHIVE"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

log "extracting archive..."
tar -C "$WORK" -xzf "$ARCHIVE"
TS_DIR=$(ls "$WORK" | head -n1)
SRC="$WORK/$TS_DIR"

# Refuse to run against a stack with existing data unless caller says so.
if docker compose ps --format json 2>/dev/null | grep -q '"State":"running"'; then
  echo "[restore] WARNING: stack is running. This will overwrite data."
  read -p "type 'yes' to continue: " confirm
  [ "$confirm" = "yes" ] || die "aborted"
fi

log "stopping stack..."
docker compose down

log "restoring env + compose..."
cp -f "$SRC/.env" .env
# docker-compose.yml is under version control; only restore if user asks.

log "recreating volumes..."
for v in postgres_data ca_data caddy_data; do
  docker volume rm "privatedns_$v" 2>/dev/null || true
  docker volume create "privatedns_$v"
done

log "restoring CA data..."
docker run --rm -v privatedns_ca_data:/data -v "$SRC":/src alpine \
  sh -c 'cd /data && tar xzf /src/ca_data.tar.gz'

log "restoring Caddy state..."
docker run --rm -v privatedns_caddy_data:/data -v "$SRC":/src alpine \
  sh -c 'cd /data && tar xzf /src/caddy_data.tar.gz'

log "starting postgres..."
docker compose up -d postgres
# wait for readiness
for _ in $(seq 1 60); do
  if docker compose exec -T postgres pg_isready >/dev/null 2>&1; then break; fi
  sleep 1
done

log "restoring postgres..."
docker compose exec -T postgres dropdb  -U "$(grep ^POSTGRES_USER .env | cut -d= -f2)" --if-exists "$(grep ^POSTGRES_DB .env | cut -d= -f2)"
docker compose exec -T postgres createdb -U "$(grep ^POSTGRES_USER .env | cut -d= -f2)" "$(grep ^POSTGRES_DB .env | cut -d= -f2)"
docker compose exec -T postgres pg_restore \
  -U "$(grep ^POSTGRES_USER .env | cut -d= -f2)" \
  -d "$(grep ^POSTGRES_DB .env | cut -d= -f2)" \
  --no-owner --no-privileges < "$SRC/postgres.dump"

log "starting full stack..."
docker compose up -d

log "done. Health: run scripts/healthcheck.sh"

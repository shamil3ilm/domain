#!/usr/bin/env bash
# seed-example.sh — creates www.example.<PRIVATE_TLD> pointing at the host
# running the stack, so the healthcheck's HTTPS assertion passes.
#
# Uses the management API (not PDNS directly). Requires ADMIN credentials
# in .env (the same ones install.sh generated).
set -euo pipefail
cd "$(dirname "$0")/.."

: "${API_URL:=http://localhost:8080}"

EMAIL=$(grep '^ADMIN_EMAIL='    .env | cut -d= -f2-)
PW=$(grep    '^ADMIN_PASSWORD=' .env | cut -d= -f2-)
TLD=$(grep   '^PRIVATE_TLD='    .env | cut -d= -f2-)
IP=$(grep    '^DNS_PUBLIC_IP='  .env | cut -d= -f2-)

echo "[seed] logging in as $EMAIL"
TOKEN=$(curl -fsS -X POST "$API_URL/api/v1/auth/login" \
   -H content-type:application/json \
   -d "{\"email\":\"$EMAIL\",\"password\":\"$PW\"}" | \
   sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ] || { echo "[seed] login failed" >&2; exit 1; }

zone="example.${TLD}"

echo "[seed] ensuring zone $zone exists"
curl -fsS -X POST "$API_URL/api/v1/zones" \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d "{\"name\":\"$zone\",\"description\":\"example private zone\"}" >/dev/null || true

for name in www app api; do
  echo "[seed] upserting $name.$zone -> $IP"
  curl -fsS -X PUT "$API_URL/api/v1/zones/$zone/records" \
    -H "authorization: bearer $TOKEN" -H content-type:application/json \
    -d "{\"name\":\"$name\",\"type\":\"A\",\"ttl\":300,\"value\":\"$IP\"}" >/dev/null
done

echo "[seed] done. Test with: dig @127.0.0.1 www.$zone A +short"

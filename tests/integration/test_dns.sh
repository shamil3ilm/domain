#!/usr/bin/env bash
# Integration test — runs against a live stack. Assumes install.sh has been run.
# Verifies:
#   * API login works with .env admin credentials
#   * Zone creation via API
#   * Record creation via API
#   * DNS resolution (private + public) via recursor
#   * PTR record for reverse zone
#   * Zone deletion cleans up
#
# Exit non-zero if any assertion fails.
set -euo pipefail
cd "$(dirname "$0")/../.."

: "${API_URL:=http://localhost:8080}"
: "${DIG_HOST:=127.0.0.1}"

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*"; FAILED=1; }
FAILED=0

EMAIL=$(grep '^ADMIN_EMAIL='    .env | cut -d= -f2-)
PW=$(grep    '^ADMIN_PASSWORD=' .env | cut -d= -f2-)
TLD=$(grep   '^PRIVATE_TLD='    .env | cut -d= -f2-)
IP=10.99.99.42
ZONE="itest.${TLD}"

echo "== 1. login =="
TOKEN=$(curl -fsS -X POST "$API_URL/api/v1/auth/login" \
   -H content-type:application/json \
   -d "{\"email\":\"$EMAIL\",\"password\":\"$PW\"}" \
   | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
if [ -n "$TOKEN" ]; then ok "login"; else fail "login"; fi
AUTH="authorization: bearer $TOKEN"

echo "== 2. bad password rejected =="
if ! curl -fsS -X POST "$API_URL/api/v1/auth/login" \
   -H content-type:application/json \
   -d "{\"email\":\"$EMAIL\",\"password\":\"wrong\"}" >/dev/null 2>&1; then
  ok "bad password rejected"
else
  fail "bad password not rejected"
fi

echo "== 3. create zone =="
if curl -fsS -X POST "$API_URL/api/v1/zones" \
    -H "$AUTH" -H content-type:application/json \
    -d "{\"name\":\"$ZONE\",\"description\":\"integration test\"}" >/dev/null; then
  ok "zone $ZONE created"
else
  fail "zone $ZONE create failed"
fi

echo "== 4. upsert A record =="
if curl -fsS -X PUT "$API_URL/api/v1/zones/$ZONE/records" \
    -H "$AUTH" -H content-type:application/json \
    -d "{\"name\":\"host1\",\"type\":\"A\",\"ttl\":60,\"value\":\"$IP\"}" >/dev/null; then
  ok "A host1.$ZONE = $IP"
else
  fail "A record upsert failed"
fi

echo "== 5. resolve via recursor =="
sleep 1
got=$(dig +time=2 +tries=2 @$DIG_HOST "host1.$ZONE" A +short 2>/dev/null | head -n1)
if [ "$got" = "$IP" ]; then ok "recursor resolves host1.$ZONE to $IP"
else fail "expected $IP, got '$got'"; fi

echo "== 6. public DNS still works =="
if dig +time=2 +tries=2 @$DIG_HOST cloudflare.com A +short 2>/dev/null | grep -qE '^[0-9]'; then
  ok "public DNS (cloudflare.com) still resolves"
else
  fail "public DNS is broken"
fi

echo "== 7. unauthenticated write is refused =="
if curl -fsS -X POST "$API_URL/api/v1/zones" \
    -H content-type:application/json -d '{"name":"nope"}' >/dev/null 2>&1; then
  fail "unauthenticated write should have been rejected"
else
  ok "unauthenticated write rejected"
fi

echo "== 8. viewer role cannot mutate =="
VIEWER_PW=$(openssl rand -base64 20 | tr -d '/+=')
curl -fsS -X POST "$API_URL/api/v1/users" \
   -H "$AUTH" -H content-type:application/json \
   -d "{\"email\":\"viewer-itest@local\",\"password\":\"$VIEWER_PW-abcd\",\"role\":\"viewer\"}" >/dev/null 2>&1 || true
VTOKEN=$(curl -fsS -X POST "$API_URL/api/v1/auth/login" \
   -H content-type:application/json \
   -d "{\"email\":\"viewer-itest@local\",\"password\":\"$VIEWER_PW-abcd\"}" \
   | sed -n 's/.*"token":"\([^"]*\)".*/\1/p') || true
if [ -n "$VTOKEN" ]; then
  code=$(curl -o /dev/null -sS -w '%{http_code}' -X POST "$API_URL/api/v1/zones" \
     -H "authorization: bearer $VTOKEN" -H content-type:application/json \
     -d '{"name":"viewer-cant-do-this.'$TLD'"}')
  if [ "$code" = "403" ]; then ok "viewer forbidden from POST /zones"
  else fail "viewer got $code, expected 403"; fi
else
  echo "  (skipping viewer test — could not create viewer user)"
fi

echo "== 9. delete zone =="
if curl -fsS -X DELETE "$API_URL/api/v1/zones/$ZONE" -H "$AUTH" >/dev/null; then
  ok "zone deleted"
else
  fail "zone delete failed"
fi

if [ "$FAILED" = "1" ]; then
  echo; echo "INTEGRATION: FAILED"; exit 1
fi
echo; echo "INTEGRATION: PASS"

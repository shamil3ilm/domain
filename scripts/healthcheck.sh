#!/usr/bin/env bash
# healthcheck.sh — verify all services are up and DNS resolves as expected.
set -euo pipefail
cd "$(dirname "$0")/.."

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAILED=1; }

FAILED=0

echo "== docker compose services =="
docker compose ps --format table

TLD=$(grep '^PRIVATE_TLD='   .env 2>/dev/null | cut -d= -f2)
DASH=$(grep '^DASHBOARD_DOMAIN=' .env 2>/dev/null | cut -d= -f2)

echo
echo "== management API =="
if curl -fsS http://localhost:8080/readyz >/dev/null 2>&1; then ok "api /readyz (direct)"; else
  # Fall back to querying via reverse-proxy on 443 (may lack cert trust; use -k).
  if curl -fskS "https://${DASH:-localhost}/readyz" >/dev/null 2>&1; then
    ok "api /readyz (via reverse-proxy, unverified TLS)"
  else
    bad "api /readyz unreachable"
  fi
fi

echo
echo "== recursor =="
if command -v dig >/dev/null; then
  if dig +time=2 +tries=1 @127.0.0.1 -p 53 example.com A +short >/dev/null 2>&1; then
    ok "recursor resolves public (example.com)"
  else
    bad "recursor did not resolve public (example.com)"
  fi

  if dig +time=2 +tries=1 @127.0.0.1 -p 53 "www.example.$TLD" A +short 2>/dev/null | grep -qE '^[0-9]'; then
    ok "recursor resolves private (www.example.$TLD)"
  else
    bad "recursor did not resolve www.example.$TLD (create the record first: see scripts/seed-example.sh)"
  fi
else
  bad "dig not installed — cannot test resolution"
fi

echo
echo "== HTTPS on private domain =="
if curl -fskS "https://www.example.$TLD" >/dev/null 2>&1; then
  ok "https://www.example.$TLD returns 200 (unverified TLS)"
else
  bad "https://www.example.$TLD unreachable"
fi

exit "$FAILED"

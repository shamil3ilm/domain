#!/usr/bin/env bash
# test-dns.sh — quick smoke test of DNS behavior. Non-destructive.
set -euo pipefail
cd "$(dirname "$0")/.."

TLD=$(grep '^PRIVATE_TLD=' .env | cut -d= -f2)
NS_HOST=127.0.0.1

echo "== recursor: public name =="
dig @${NS_HOST} google.com A +short || true
echo

echo "== recursor: private name =="
dig @${NS_HOST} www.example.${TLD} A +short || true
dig @${NS_HOST} app.example.${TLD} A +short || true
echo

echo "== recursor: NXDOMAIN in private zone =="
dig @${NS_HOST} does-not-exist.${TLD} A
echo

echo "== authoritative direct query =="
dig @${NS_HOST} -p "$(grep '^DNS_AUTH_PORT=' .env | cut -d= -f2 || echo 5353)" ${TLD} SOA +short || true

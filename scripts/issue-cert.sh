#!/usr/bin/env bash
# issue-cert.sh <fqdn> — issue a certificate from the private CA for an
# ad-hoc service outside Docker. Writes ./certs/<fqdn>.crt and .key.
set -euo pipefail
cd "$(dirname "$0")/.."

FQDN="${1:-}"
[ -n "$FQDN" ] || { echo "usage: $0 <fqdn>"; exit 2; }

mkdir -p certs

# Uses step-ca via a one-shot 'step' container so the host doesn't need step
# installed. Requires the CA to be up.
docker run --rm --network privatedns_privatedns \
  -v "$(pwd)/certs:/out" \
  smallstep/step-cli:0.27.4 sh -c "
    step ca bootstrap --ca-url https://ca:9000 --fingerprint \$(cat /out/.fingerprint 2>/dev/null || true) --force >/dev/null 2>&1 || true
    step ca certificate '$FQDN' /out/$FQDN.crt /out/$FQDN.key --provisioner acme --san '$FQDN'
  "

echo "[issue-cert] wrote certs/$FQDN.crt and certs/$FQDN.key"

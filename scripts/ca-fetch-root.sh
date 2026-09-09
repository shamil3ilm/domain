#!/usr/bin/env bash
# ca-fetch-root.sh — download the private CA's root certificate. Install this
# on any client that should trust *.<PRIVATE_TLD> HTTPS services.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="${1:-privatedns-root.crt}"

# The CA publishes its root at /roots.pem over HTTPS. We use --insecure here
# because the client (this script) doesn't yet trust the CA. The fingerprint
# should be verified out-of-band before installing.
CA_HOST=$(grep '^CA_DNS_NAME=' .env | cut -d= -f2)

# Try via reverse-proxy first (uses proper hostname), then direct 9000.
curl -fskS "https://${CA_HOST}:9000/roots.pem" -o "$OUT" \
  || curl -fskS "https://localhost:9000/roots.pem" -o "$OUT"

echo "[ca-fetch-root] wrote $OUT"
echo "[ca-fetch-root] SHA-256: $(openssl x509 -in "$OUT" -noout -fingerprint -sha256 | cut -d= -f2)"
echo
echo "  Install on Linux:   sudo cp $OUT /usr/local/share/ca-certificates/ && sudo update-ca-certificates"
echo "  Install on macOS:   sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain $OUT"
echo "  Install on Windows: certutil -addstore -f Root $OUT   (Admin PowerShell)"

#!/bin/sh
# Fetch the CA's root certificate so Caddy trusts step-ca's ACME endpoint.
set -eu

: "${CA_DNS_NAME:?}"

# Wait for step-ca to publish its root and become reachable.
i=0
while [ $i -lt 60 ]; do
  if wget -qO /usr/local/share/ca-certificates/privatedns-ca.crt \
       "https://${CA_DNS_NAME}:9000/roots.pem" --no-check-certificate 2>/dev/null; then
    break
  fi
  i=$((i+1)); sleep 1
done
if [ ! -s /usr/local/share/ca-certificates/privatedns-ca.crt ]; then
  echo "reverse-proxy: warning — could not fetch privatedns root cert; ACME may fail" >&2
fi

update-ca-certificates 2>/dev/null || true

exec "$@"

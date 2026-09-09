#!/bin/sh
# step-ca bootstrap. On first boot, generate a root + intermediate CA and an
# ACME provisioner. On subsequent boots, just start.
set -eu

: "${CA_PASSWORD:?}"
: "${CA_DNS_NAME:?}"
: "${PRIVATE_TLD:?}"

STEPPATH=/home/step
export STEPPATH

if [ ! -f "${STEPPATH}/config/ca.json" ]; then
  echo "ca: initializing new CA..."

  # Password file — step-ca reads the password to unlock signing keys from here.
  mkdir -p "${STEPPATH}/secrets"
  printf "%s" "${CA_PASSWORD}" > "${STEPPATH}/secrets/password"
  chmod 400 "${STEPPATH}/secrets/password"

  # Init a two-tier CA: offline root, online intermediate.
  step ca init \
    --name "privatedns-ca" \
    --dns "${CA_DNS_NAME},ca,localhost" \
    --address ":9000" \
    --provisioner "admin@${PRIVATE_TLD}" \
    --password-file "${STEPPATH}/secrets/password" \
    --provisioner-password-file "${STEPPATH}/secrets/password" \
    --deployment-type standalone

  # Add an ACME provisioner so Caddy (and anyone else who wants automated
  # cert issuance) can enroll without a JWK.
  step ca provisioner add acme --type ACME

  # Add a JWK provisioner Caddy could also use, and keep the admin one for
  # step-cli operations.
  echo "ca: initialized."
fi

# The root cert path is stable; publish it via /home/step/certs/root_ca.crt.
# Callers (Caddy, install scripts) mount ca_data and read it from there.

exec /usr/local/bin/step-ca \
  --password-file "${STEPPATH}/secrets/password" \
  "${STEPPATH}/config/ca.json"

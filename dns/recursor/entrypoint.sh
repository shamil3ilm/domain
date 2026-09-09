#!/bin/sh
# Render unbound.conf from the template. Wait for pdns-auth to be reachable
# by hostname (so DNS resolution during startup works), then start unbound.
set -eu

: "${PRIVATE_TLD:?}"
: "${RECURSOR_ACL:?}"
: "${RECURSOR_UPSTREAMS:?}"

# ---- Fetch DNSSEC root trust anchor on first boot ---------------------------
if [ ! -f /var/lib/unbound/root.key ]; then
  unbound-anchor -a /var/lib/unbound/root.key || true
  chown unbound:unbound /var/lib/unbound/root.key || true
fi

# ---- Resolve pdns-auth once so we can hardcode its IP in the forward-zone ---
# Container DNS gives us pdns-auth's address. If we passed just the hostname to
# unbound it would try to resolve it through ITSELF, which is a chicken-and-egg.
PDNS_AUTH_IP=""
i=0
while [ $i -lt 60 ] && [ -z "$PDNS_AUTH_IP" ]; do
  PDNS_AUTH_IP=$(getent ahostsv4 pdns-auth 2>/dev/null | awk 'NR==1{print $1}')
  [ -z "$PDNS_AUTH_IP" ] && sleep 1
  i=$((i+1))
done
if [ -z "$PDNS_AUTH_IP" ]; then
  echo "recursor: could not resolve pdns-auth via container DNS" >&2
  exit 1
fi

# ---- Build ACL block --------------------------------------------------------
RECURSOR_ACL_LINES=""
oldIFS=$IFS; IFS=","
for cidr in $RECURSOR_ACL; do
  RECURSOR_ACL_LINES="${RECURSOR_ACL_LINES}    access-control: ${cidr} allow
"
done
IFS=$oldIFS

# ---- Build upstream block ---------------------------------------------------
# RECURSOR_UPSTREAMS format: ip@port#tls-name,ip@port#tls-name
RECURSOR_UPSTREAM_LINES=""
oldIFS=$IFS; IFS=","
for up in $RECURSOR_UPSTREAMS; do
  RECURSOR_UPSTREAM_LINES="${RECURSOR_UPSTREAM_LINES}    forward-addr: ${up}
"
done
IFS=$oldIFS

export RECURSOR_ACL_LINES RECURSOR_UPSTREAM_LINES

# Render config.
envsubst < /etc/unbound/unbound.conf.template > /etc/unbound/unbound.conf

# Patch the forward-addr line for the private TLD with pdns-auth's real IP.
sed -i "s|forward-addr: 172.28.0.0@53|forward-addr: ${PDNS_AUTH_IP}@53|" /etc/unbound/unbound.conf

# Sanity-check.
unbound-checkconf /etc/unbound/unbound.conf

exec "$@"

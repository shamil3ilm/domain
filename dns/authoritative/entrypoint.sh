#!/bin/sh
# Render pdns.conf from the template, waiting for postgres, then exec pdns_server.
set -eu

: "${PGHOST:?}"; : "${PGPORT:?}"; : "${PGDATABASE:?}"; : "${PGUSER:?}"; : "${PGPASSWORD:?}"
: "${PDNS_API_KEY:?}"

# Wait for postgres to accept connections and for our schema to exist.
i=0
until pg_isready -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" >/dev/null 2>&1; do
  i=$((i+1))
  if [ $i -gt 60 ]; then
    echo "postgres never became ready" >&2
    exit 1
  fi
  sleep 1
done

# Render config.
envsubst < /etc/powerdns/pdns.conf.template > /etc/powerdns/pdns.conf
chmod 640 /etc/powerdns/pdns.conf
chown root:pdns /etc/powerdns/pdns.conf

exec "$@"

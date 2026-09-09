# PowerDNS Authoritative with PostgreSQL backend + HTTP API.
# We use the official image and layer in our config + entrypoint.
FROM powerdns/pdns-auth-49:latest

USER root

RUN apt-get update && apt-get install -y --no-install-recommends \
        gettext-base \
        postgresql-client \
    && rm -rf /var/lib/apt/lists/*

COPY pdns.conf.template /etc/powerdns/pdns.conf.template
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# PowerDNS drops privileges internally. Entrypoint runs as root only long
# enough to render config from env vars, then execs pdns_server as pdns.
ENTRYPOINT ["/entrypoint.sh"]
CMD ["pdns_server", "--config-dir=/etc/powerdns", "--daemon=no"]

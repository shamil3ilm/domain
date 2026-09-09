# Unbound recursor. Alpine's package is well-maintained.
FROM alpine:3.20

RUN apk add --no-cache \
        unbound \
        ldns \
        drill \
        gettext \
        ca-certificates \
        tini \
    && adduser -D -H -s /sbin/nologin unbound || true \
    && mkdir -p /var/lib/unbound /etc/unbound/conf.d \
    && chown -R unbound:unbound /var/lib/unbound /etc/unbound

COPY unbound.conf.template /etc/unbound/unbound.conf.template
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
CMD ["unbound", "-d", "-c", "/etc/unbound/unbound.conf"]

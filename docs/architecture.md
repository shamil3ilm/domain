# Architecture

## Overview

```
┌────────────────────────── privatedns compose stack ──────────────────────────┐
│                                                                              │
│    ┌────────────┐        ┌──────────────┐         ┌─────────────────────┐    │
│    │  clients   │  →53   │   recursor   │ →53     │   pdns-auth         │    │
│    │  (LAN/VPN) │        │   (Unbound)  ├────────►│  (PowerDNS Auth.)   │    │
│    │  DNS = us  │        │              │         │  http api :8081     │    │
│    └────────────┘        │              │         └──────────┬──────────┘    │
│                          │              │ →853              PATCH/POST│      │
│                          │  DoT upstream│                    │        │      │
│                          │  (1.1.1.1,   │                    │        │      │
│                          │   9.9.9.9)   │                    │        │      │
│                          └──────────────┘                    │        │      │
│                                                              │        │      │
│                                                     ┌────────▼───┐    │      │
│                                                     │ postgres   │    │      │
│                                                     │ pdns +     │◄───┘      │
│                                                     │ mgmt schemas          │
│                                                     └────┬───────┘           │
│                                                          │                   │
│                                    ┌──────────────┐      │                   │
│  https://dashboard.myworld    ─────►  reverse-    │      │                   │
│  https://api.myworld          ─────►    proxy    ─┼──────►    api (Go)       │
│  https://www.example.myworld  ─────►   (Caddy)    │      │  /api/v1/*        │
│                                    │              │      │  bootstraps admin │
│                                    │  ACME        │      │  + private root   │
│                                    │  ──────►     │      │                   │
│                                    │              │      │                   │
│                                    │              │      │                   │
│                                    └──────┬───────┘      │                   │
│                                           │              │                   │
│                                    ┌──────▼──────┐       │                   │
│                                    │   step-ca   │───────┘                   │
│                                    │  (private   │                           │
│                                    │  CA + ACME) │                           │
│                                    └─────────────┘                           │
│                                                                              │
│         ┌────────────┐    scrape    ┌────────────┐                           │
│         │ prometheus ├──────────────► pdns-auth  │                           │
│         │            │◄─────────────┤    api     │                           │
│         └─────┬──────┘              └────────────┘                           │
│               │                                                              │
│         ┌─────▼──────┐                                                       │
│         │  grafana   │                                                       │
│         └────────────┘                                                       │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

## Request flow

### Client resolves `www.example.myworld`

1. Client sends UDP query to the recursor (127.0.0.1:53 or your VPN's DNS).
2. Recursor sees the query name ends in `.myworld` — its config has a
   `forward-zone` for that TLD pointing at `pdns-auth`.
3. `pdns-auth` looks up the A record in Postgres and answers.
4. Recursor returns the answer to the client, cached for the record's TTL.

### Client resolves `github.com`

1. Same recursor.
2. No forward-zone matches → recursor forwards the query to its upstream DoT
   resolvers (Cloudflare, Quad9) with DNSSEC validation.

### Admin creates a record via the dashboard

1. Browser POSTs to `https://dashboard.myworld/api/v1/zones/example.myworld/records`.
2. Caddy proxies to `api:8080`.
3. API validates the JWT + role (Operator or Admin), normalizes the record
   content, and calls `PATCH /servers/localhost/zones/example.myworld.` on
   PowerDNS's HTTP API.
4. PowerDNS writes to the `pdns` schema in Postgres and bumps the zone serial.
5. API writes an audit event to `mgmt.audit_log`.
6. Next query for that name hits the fresh record — no reload, no zone-file
   edit, no restart.

### Caddy obtains a cert for `www.example.myworld`

1. On first request, Caddy has no cert. It initiates ACME against
   `https://ca:9000/acme/acme/directory`.
2. step-ca issues a challenge; Caddy solves it (HTTP-01 by default).
3. step-ca issues the certificate signed by the intermediate CA.
4. Caddy caches the cert in `caddy_data`. Renewal is automatic.

## Data model

Two independent schemas in one Postgres database:

- **`pdns`** — PowerDNS's own schema (`domains`, `records`, `cryptokeys`,
  `tsigkeys`, etc.). Migrated directly from the upstream reference schema so
  PowerDNS upgrades don't require code changes on our side.
- **`mgmt`** — our schema. `users`, `api_keys`, `zone_meta`, `audit_log`,
  `revoked_tokens`, `recent_queries`.

The management API is the only writer to `mgmt`. It reads from `pdns.domains`
via joins for enrichment but never writes to `pdns.*` — those writes always go
through the PowerDNS API. This keeps PDNS's internal invariants (serial bumps,
NSEC chains, notify triggers) intact.

## Security boundaries

- **Recursor** — refuses queries outside `RECURSOR_ACL`. Never open-recursive.
- **Authoritative** — port 53 published on host `${DNS_AUTH_PORT}` (5353 by
  default) for secondary transfers. Its HTTP API (`:8081`) is unpublished —
  reachable only from inside the compose network.
- **API** — never published to the host directly. Only reachable via Caddy,
  which terminates TLS.
- **CA private keys** — encrypted at rest, unlocked with `CA_PASSWORD`.
  Password never leaves the CA container.

## Extensibility

- **Adding a public domain** (e.g. `example.com`): create the zone via API.
  The recursor's forward-zone only intercepts `${PRIVATE_TLD}`; everything else
  is unaffected. For `example.com` to actually serve to the Internet you'd
  need to delegate `example.com` at your registrar to `pdns-auth`'s public IP.
- **Secondary DNS** — configure a secondary PowerDNS or nsd instance to AXFR
  from `pdns-auth`. See `docs/ha.md`.
- **Kubernetes** — the compose file is a starting point; each service maps 1:1
  to a Deployment/StatefulSet.

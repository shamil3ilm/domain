# High availability

The default deployment is a single-host stack. Two ways to make DNS resilient:

## Option A — Multiple recursors, one authoritative

Cheapest and most impactful. DNS resolution failures usually come from the
recursor being unavailable, not the authoritative. Run an extra recursor on
another host (or several) with the same forwarding config, and hand out both
IPs as DNS to your clients.

Steps:

1. Copy `dns/recursor/` to another host.
2. In its `unbound.conf`, set the `forward-addr` for `${PRIVATE_TLD}` to
   the primary host's IP (published on `${DNS_AUTH_PORT}`, default 5353).
3. Firewall: allow inbound 5353 UDP/TCP from the secondary recursor's IP on
   the primary. This is how the recursor reaches the authoritative directly.
4. Push both recursor IPs to clients (see `docs/client-setup.md`).

## Option B — Multiple authoritative servers

If you also want the authoritative server to be redundant, deploy secondaries
that AXFR zones from the primary.

### Set up the primary for zone transfers

In `dns/authoritative/pdns.conf.template`, add:

```
allow-axfr-ips=<SECONDARY_IP>/32
also-notify=<SECONDARY_IP>
```

Rebuild and restart `pdns-auth`.

### Deploy a secondary

Any DNS server that speaks AXFR + NOTIFY works. Options:

- **PowerDNS "Slave"** — same image, different config. Set
  `slave=yes`, `master=no`, and `superslave=yes` if you want auto-provisioning
  of zones as they appear on the primary. Point it at your Postgres (fresh
  instance) or use SQLite for a self-contained secondary.
- **nsd** — lightweight, easy to configure.
- **Knot DNS** — high-performance.

Each secondary you add needs:

1. Its IP added to `allow-axfr-ips` on the primary.
2. TSIG configured on both sides if you want authenticated transfers
   (recommended over relying only on IP ACLs). Manage TSIG keys with the
   `pdnsutil` CLI inside `pdns-auth`:

   ```
   docker compose exec pdns-auth pdnsutil generate-tsig-key priv-xfer hmac-sha256
   docker compose exec pdns-auth pdnsutil add-meta example.myworld TSIG-ALLOW-AXFR priv-xfer
   ```

3. Its NS record advertised in the parent zone (so recursors know it exists).

### Client-side round-robin

Point clients at both authoritatives *if* you're also delegating public zones
here. For the private TLD, since we control the recursors, clients only need
to know the recursor(s).

## Option C — Postgres HA

Postgres is the single point of storage. Options:

- **Patroni / repmgr** for streaming replication + automatic failover.
- **CloudSQL / RDS / Neon** if you're OK with a cloud dependency.
- **pgBackRest** for point-in-time recovery.

We don't bundle these — pick whichever suits your ops posture. The management
API and PowerDNS both take standard `libpq`-style env vars, so pointing them
at a floating IP or PgBouncer is a matter of changing `PGHOST`.

## Recommendation

For most private-DNS use cases, **Option A** is enough. DNS clients cache
aggressively; a single-host authoritative that occasionally reboots is fine
if two recursors on separate hosts absorb it.

Add Option B when you have multiple sites (put a full auth+recursor stack in
each site, with the auth in a secondary role at all but one site) or when the
authoritative is a compliance requirement.

# Troubleshooting

## `dig @<host> www.example.myworld` returns nothing

Check in order:

1. **Recursor is up:** `docker compose ps recursor` should show healthy.
2. **Recursor is reachable:** `dig @<host> id.server CH TXT +short` should
   return `privatedns-recursor`. If this fails, the recursor isn't listening
   on the interface you're querying.
3. **Your IP is in the ACL:** `.env`'s `RECURSOR_ACL`. If not, add your
   subnet and restart the recursor.
4. **The zone exists:** `curl -H "authorization: bearer $TOKEN"
   http://localhost:8080/api/v1/zones` — does `example.myworld` appear?
5. **The record exists in that zone:** dashboard → Records → pick the zone.
6. **The authoritative can be reached from the recursor:**

   ```bash
   docker compose exec recursor drill @pdns-auth example.myworld SOA
   ```

## `dig @<host> google.com` returns nothing but private names work

Recursor can reach the auth but not the upstream. Usually a network issue
between the compose host and `1.1.1.1:853` (DoT). Test:

```
docker compose exec recursor drill @1.1.1.1 example.com
```

If DoT is blocked by your firewall/ISP, switch to plaintext upstreams (bad
idea) or find a different upstream. Edit `RECURSOR_UPSTREAMS` in `.env`.

## `curl https://www.example.myworld` gives a TLS error

- If it says "self-signed" / "unknown issuer": you haven't installed the CA
  root on this client. See `docs/client-setup.md`.
- If it says "hostname mismatch": Caddy issued a cert for a different name.
  Check `reverse-proxy/Caddyfile` — the vhost block must match the hostname
  the client is using.
- If it says "connection refused": the reverse-proxy container isn't up, or
  its port 443 isn't published. `docker compose ps reverse-proxy`.

## `docker compose up -d` fails immediately

- `port is already allocated` → something else is using 53/80/443. Kill it or
  change the mapping in `.env` + `docker-compose.yml`.
- On Linux, `systemd-resolved` grabs 53 by default. Disable stub-listener:
  edit `/etc/systemd/resolved.conf` → `DNSStubListener=no`, `sudo systemctl
  restart systemd-resolved`.

## Caddy can't get certs from step-ca

Look at logs:

```
docker compose logs reverse-proxy --tail=200
docker compose logs ca --tail=200
```

Common causes:

- **CA root not trusted by Caddy container.** The entrypoint fetches the root
  from step-ca on start. If step-ca wasn't ready yet, this fails. Restart
  the reverse-proxy container: `docker compose restart reverse-proxy`.
- **Domain not resolvable inside compose network.** Caddy resolves the vhost
  hostname when solving HTTP-01; make sure the hostname (`dashboard.myworld`,
  etc.) has a Docker alias — see the `networks.privatedns.aliases` field in
  `docker-compose.yml`.

## `pdns-auth` won't start

Almost always a Postgres connection problem or a schema mismatch. Logs:

```
docker compose logs pdns-auth --tail=200
```

If it complains about missing tables, the migrations didn't run. Compose
runs `database/migrations/*.sql` **only when Postgres initializes a fresh
data directory.** For an existing volume, run the migrations by hand:

```
docker compose exec -T postgres psql -U privatedns -d privatedns < database/migrations/0001_pdns_schema.sql
docker compose exec -T postgres psql -U privatedns -d privatedns < database/migrations/0002_mgmt_schema.sql
```

## API returns 401 for a valid token

- Token expired (12h lifetime for JWTs). Log in again.
- Token was revoked (via `/auth/logout` from another session, or admin
  action). Log in again.
- `API_JWT_SECRET` was rotated. All tokens become invalid; log in again.

## The audit log fills up

Add a retention cron:

```sql
DELETE FROM mgmt.audit_log WHERE ts < now() - interval '90 days';
```

## Everything's slow

- Postgres CPU: run `EXPLAIN` on the slow query. Add indexes if needed. The
  default schema is indexed for typical loads (thousands of records, tens
  of thousands of audit rows).
- Recursor cache-hit ratio: `docker compose exec recursor unbound-control stats_noreset | grep cachehits`. Low ratio means TTLs are too short or clients aren't sticky.
- PDNS query rate: check the Grafana overview dashboard.

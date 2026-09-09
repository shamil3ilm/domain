# Operations

Day-2 activities: adding zones, records, users, servers; using the API for
automation; adding vhosts behind Caddy.

## Adding a zone

**Dashboard:** Zones → enter name → Create.

**API:**

```bash
curl -X POST https://api.myworld/api/v1/zones \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"home.myworld","description":"my home LAN"}'
```

When the zone is under the private TLD, an `NS` record is automatically added
in the parent zone so the recursor's forward-zone resolves it correctly.

## Adding records

**Dashboard:** Records → pick zone → fill the form.

**API — single-value:**

```bash
curl -X PUT https://api.myworld/api/v1/zones/home.myworld/records \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"nas","type":"A","value":"10.10.0.50","ttl":300}'
```

**API — multi-value (e.g. two A records at the same name):**

```bash
curl -X PUT https://api.myworld/api/v1/zones/home.myworld/records \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"nas","type":"A","content":["10.10.0.50","10.10.0.51"]}'
```

The `PUT` is REPLACE semantics — sending `content:[...]` overwrites the
existing record-set for that (name, type).

**Delete:**

```bash
curl -X DELETE 'https://api.myworld/api/v1/zones/home.myworld/records?name=nas&type=A' \
  -H "authorization: bearer $TOKEN"
```

## Reverse DNS

Create a reverse zone for your private range and add PTR records:

```bash
# For 10.10.0.0/16:
curl -X POST https://api.myworld/api/v1/zones \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"10.10.in-addr.arpa"}'

# PTR for 10.10.0.50 -> nas.home.myworld
curl -X PUT https://api.myworld/api/v1/zones/10.10.in-addr.arpa/records \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"50.0","type":"PTR","value":"nas.home.myworld"}'
```

You'll also need to configure your recursor to consult the private auth for
that reverse zone. `unbound.conf.template` already forwards the whole
`in-addr.arpa` for RFC1918 space to the auth server if you enable the
`private-address` and `local-zone` directives — uncomment in the template and
rebuild.

## Adding an HTTPS vhost behind Caddy

Edit `reverse-proxy/Caddyfile`, add a block like:

```
grafana.myworld {
	reverse_proxy grafana:3000
}
```

Reload without restart:

```bash
docker compose exec reverse-proxy caddy reload --config /etc/caddy/Caddyfile
```

Caddy will auto-issue a certificate from step-ca on first request.

## API key for automation

Prefer API keys over JWTs for automation. From the dashboard: **API Keys →
New key**. The token is shown once; copy it. Use as:

```bash
curl -H "authorization: bearer pd_ABC..." https://api.myworld/api/v1/zones
```

Revoke a key from the dashboard when it's no longer needed.

## Adding a secondary DNS server

See `docs/ha.md`.

## Adding a public domain

Say you buy `example.com`.

1. In `example.com`'s registrar, delegate to your `DNS_PUBLIC_IP` (glue: `ns1.myworld` isn't a
   valid public NS, so either use a real hostname or point `ns1.example.com A
   <DNS_PUBLIC_IP>` and use that as the NS).
2. Create the zone via API just like a private one.
3. Because `example.com` is not under `${PRIVATE_TLD}`, the recursor forwards
   *upstream* for it. That's fine — everyone on the Internet including your
   clients will reach `pdns-auth` via public DNS delegation.
4. Get a public certificate for `example.com` (e.g. via Let's Encrypt in
   Caddy). See Caddy's docs.

## Metrics + dashboards

- Prometheus: http://\<host\>:9090
- Grafana: http://\<host\>:3000 (login with `GRAFANA_ADMIN_*` from `.env`).
  A minimal "privatedns / Overview" dashboard is provisioned automatically.

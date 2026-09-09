# Installation

## Prerequisites

- Linux host (tested with Ubuntu 22.04+, Debian 12+, Fedora 39+, Alpine).
  macOS with Docker Desktop works for evaluation but is not recommended for
  production.
- Docker Engine 24+ with the `compose` plugin.
- ≥ 2 GB RAM, ≥ 10 GB disk.
- Ports 53 (UDP+TCP), 80, 443 free on the host. Optionally: 5353 (secondary
  AXFR), 9000 (CA), 3000 (Grafana), 9090 (Prometheus).
- `openssl` and `dig` installed for the install script and healthcheck.

## Install

```bash
git clone <repo> privatedns && cd privatedns
./scripts/install.sh
```

The install script:

1. Copies `.env.example` → `.env` if it doesn't exist.
2. Generates strong random values for `POSTGRES_PASSWORD`, `PDNS_API_KEY`,
   `API_JWT_SECRET`, `CA_PASSWORD`, `ADMIN_PASSWORD`, and
   `GRAFANA_ADMIN_PASSWORD`.
3. Runs `docker compose build`.
4. Runs `docker compose up -d`.
5. Waits for services to report healthy.
6. Prints the admin credentials.

Review `.env` **before** first boot if you want a different `PRIVATE_TLD`,
`DNS_PUBLIC_IP`, or ACLs. Once the CA has generated its root key material,
changing `CA_DNS_NAME` requires wiping `ca_data`.

## Post-install hardening checklist

- [ ] Log into the dashboard and change the admin password (or set a new one
      before first boot).
- [ ] Create a per-person account for each operator; delete or disable the
      shared bootstrap account.
- [ ] Firewall: only allow the recursor's port 53 from your VPN / LAN.
      Never expose it to the public Internet.
- [ ] Firewall the API (`api:8080` isn't published, but if you expose Caddy
      publicly, restrict `dashboard.*` and `api.*` to trusted IPs).
- [ ] Install `scripts/backup.sh` on a cron: `0 3 * * *  /path/to/scripts/backup.sh`.
- [ ] Off-site copy of backups.
- [ ] Distribute the CA root cert (`scripts/ca-fetch-root.sh`) to devices that
      should trust internal HTTPS.

## Firewall (nftables example)

```
# Allow DNS from your VPN subnet only.
table inet filter {
  chain input {
    type filter hook input priority 0; policy drop;
    iif "lo" accept
    ct state established,related accept

    ip  saddr 10.10.0.0/16 udp dport 53 accept
    ip  saddr 10.10.0.0/16 tcp dport 53 accept
    ip  saddr 10.10.0.0/16 tcp dport 443 accept
    ip  saddr 10.10.0.0/16 tcp dport 80  accept    # HTTP-01 challenges
    ip  saddr 10.10.0.0/16 tcp dport 9000 accept   # step-ca ACME (optional)

    tcp dport 22 accept
  }
}
```

## Updating

```bash
git pull
docker compose build
docker compose up -d
```

Postgres migrations run automatically when a fresh Postgres data volume boots,
but not on subsequent boots. To apply new migrations to an existing DB, place
them in `database/migrations/` numbered above existing ones and run them
manually or use a migration tool like `golang-migrate` (not bundled — kept
minimal on purpose).

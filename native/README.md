# privatedns (native, no-Docker build)

A single-binary private DNS + management API. No Docker, no external database,
no external CA. Just one executable and a data directory.

Runs on Windows, Linux, macOS, and anything Go targets. Cross-compiles trivially.

## What it does

- **Authoritative DNS** for zones you create (any private TLD you like).
- **Recursive forwarder** for everything else — your public DNS keeps working.
- **Management HTTP API** for zones, records, users, API keys, audit log.
- **Web dashboard** (embedded in the binary).
- **RBAC** (admin / operator / viewer) with JWT + API-key auth.
- **SQLite** for storage — a single file, easy to back up.

## What it does *not* do (yet)

Compared to the Docker version at `../`, this one skips:
- DNSSEC (no key management)
- Zone transfers to external secondaries
- Automatic HTTPS via ACME (the API is HTTP by default — put it behind your
  own TLS reverse proxy or run it on loopback + VPN)
- Prometheus/Grafana monitoring stack

Everything else — the DNS authority, the recursor, zone/record CRUD, RBAC,
audit, dashboard — is here and works.

## Quick start

Prereqs: **Go 1.23+** (only for building; the binary is standalone).

```powershell
# From C:\domain\native
go build -o privatedns.exe .

# Run it. Use high ports on Windows — the OS reserves 53/5353.
$env:PRIVATEDNS_DATA_DIR       = "C:\domain\native\data"
$env:PRIVATEDNS_DNS_ADDR       = "127.0.0.1:15353"
$env:PRIVATEDNS_API_ADDR       = "127.0.0.1:18080"
$env:PRIVATEDNS_PRIVATE_TLD    = "myworld"
$env:PRIVATEDNS_ADMIN_EMAIL    = "admin@local"
$env:PRIVATEDNS_ADMIN_PASSWORD = "your-strong-password"
.\privatedns.exe
```

On Linux/macOS:

```bash
go build -o privatedns .
sudo PRIVATEDNS_ADMIN_PASSWORD='...' ./privatedns
# (sudo only needed to bind port 53. Use a high port and skip sudo otherwise.)
```

Open http://localhost:18080/ for the dashboard.

## End-to-end verification

Once running, from another shell:

```powershell
$base = "http://127.0.0.1:18080"
$tok = (Invoke-RestMethod -Uri "$base/api/v1/auth/login" -Method POST `
        -Body (@{email='admin@local';password='your-strong-password'} | ConvertTo-Json) `
        -ContentType 'application/json').token
$h = @{Authorization="Bearer $tok"}

# Create a zone
Invoke-RestMethod -Uri "$base/api/v1/zones" -Method POST -Headers $h `
  -Body (@{name='example.myworld';description='test'} | ConvertTo-Json) `
  -ContentType 'application/json'

# Add a record
Invoke-RestMethod -Uri "$base/api/v1/zones/example.myworld/records" -Method PUT -Headers $h `
  -Body (@{name='www';type='A';value='10.99.99.42';ttl=60} | ConvertTo-Json) `
  -ContentType 'application/json'

# Query it
.\dig.exe -s 127.0.0.1:15353 -t A www.example.myworld
# => www.example.myworld. 60 IN A 10.99.99.42
```

## Pointing your machine at it

**Only useful when the DNS server is bound to a real port (53).** On Windows,
that means running as Administrator or using [`netsh` port-forwarding rules](https://learn.microsoft.com/en-us/windows-server/networking/technologies/netsh/netsh-interface-portproxy).
Easier path: run this on a Linux box (Pi, VM, WSL2, etc.) where port 53 is
free, and point Windows clients at that host's IP.

### Configure your Windows client

```powershell
Set-DnsClientServerAddress -InterfaceAlias "Wi-Fi" -ServerAddresses ("<host-ip>","1.1.1.1")
```

### Configure a Linux client

Edit `/etc/systemd/resolved.conf` — set `DNS=<host-ip>` and
`Domains=~myworld`. Restart resolved.

### Configure your router (easiest)

Set the LAN DHCP DNS to your privatedns host IP. Every device on the network
inherits it.

## Configuration reference

All optional; sensible defaults.

| Variable                       | Default                     | Description                                    |
|--------------------------------|-----------------------------|------------------------------------------------|
| `PRIVATEDNS_DATA_DIR`          | `./data`                    | Where SQLite DB + JWT key live                 |
| `PRIVATEDNS_PRIVATE_TLD`       | `myworld`                   | Your private namespace                         |
| `PRIVATEDNS_DNS_ADDR`          | `:53`                       | DNS listen address                             |
| `PRIVATEDNS_API_ADDR`          | `:8080`                     | HTTP API + dashboard listen address            |
| `PRIVATEDNS_UPSTREAMS`         | `1.1.1.1:53,9.9.9.9:53`     | Comma-separated upstream resolvers             |
| `PRIVATEDNS_ADMIN_EMAIL`       | `admin@local`               | Bootstrap admin email                          |
| `PRIVATEDNS_ADMIN_PASSWORD`    | *generated on first boot*   | Bootstrap admin password (printed once)        |
| `PRIVATEDNS_ALLOW_FROM`        | *all*                       | CIDRs allowed to query DNS (empty = allow all) |
| `PRIVATEDNS_LOG_LEVEL`         | `info`                      | debug / info / warn / error                    |

## Backup / restore

Everything's in the data dir. To back up:

```powershell
Copy-Item C:\domain\native\data C:\backups\privatedns-2026-09-09 -Recurse
```

To restore: stop the binary, replace the data dir, start.

For point-in-time backups without stopping the service:

```powershell
# SQLite's WAL mode makes this safe.
Copy-Item C:\domain\native\data\privatedns.db C:\backups\privatedns.db
```

## Which version should I use?

- **This native version** if you want to try it right now on a laptop, don't
  have Docker, or want a single binary you can drop on a Pi.
- **The Docker version** (`../`) if you want DNSSEC, ACME-issued internal
  certs, Prometheus/Grafana observability, or plan to eventually run
  secondary DNS on other hosts.

You can start with this one and migrate to the Docker version later without
losing zones/records — the API contract is identical, so `curl` a dump from
one and `curl` it into the other.

## Layout

```
native/
├── main.go                      entry point
├── cmd/dig/                     tiny DNS test client
├── internal/
│   ├── config/                  env → Config
│   ├── store/                   SQLite + all queries
│   ├── dnssrv/                  authoritative + recursor
│   ├── httpapi/                 REST API + middleware
│   ├── dashboard/               embedded SPA
│   └── bootstrap/               first-boot seeding
└── data/                        (created at runtime)
    ├── privatedns.db            SQLite database
    └── jwt.key                  JWT signing secret
```

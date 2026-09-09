# privatedns

A self-hosted private DNS + domain platform. You choose a namespace (e.g.
`.myworld`), stand up this stack, and clients on your LAN/VPN can create,
resolve, and serve names inside it — with proper HTTPS via a bundled private
Certificate Authority.

This is **not** an Internet registry. Public DNS keeps working normally for
`google.com`, `github.com`, etc. Only clients configured to use this
recursor resolve the private namespace.

---

## What's inside

| Service         | Role                                          | Image                     |
|-----------------|-----------------------------------------------|---------------------------|
| `postgres`      | Storage for PowerDNS + management schema      | `postgres:16-alpine`      |
| `pdns-auth`     | Authoritative DNS for the private namespace   | `powerdns/pdns-auth-49`   |
| `recursor`      | Recursive resolver for clients                | Alpine + `unbound`        |
| `ca`            | Private Certificate Authority (ACME provider) | `smallstep/step-ca`       |
| `api`           | Management API (Go)                           | built from `./api`        |
| `reverse-proxy` | HTTPS termination + dashboard + example vhosts| `caddy`                   |
| `prometheus`    | Metrics                                       | `prom/prometheus`         |
| `grafana`       | Dashboards                                    | `grafana/grafana`         |

## Why this stack

- **PowerDNS Authoritative** — HTTP API is the killer feature. Every zone/record
  mutation goes through PowerDNS's own REST API, so the management layer never
  edits zone files or reloads BIND. PostgreSQL backend means DR is a pg_dump.
  Native DNSSEC. Actively maintained.
- **Unbound** — the reference recursive resolver. Configurable to forward the
  private TLD to `pdns-auth` and resolve everything else via DNS-over-TLS to
  upstream resolvers (Cloudflare, Quad9 by default).
- **step-ca** — a real CA with an ACME endpoint. Caddy uses it to auto-issue
  certificates for private-TLD hostnames. Same enrollment flow you'd use with
  Let's Encrypt in production, just against a CA you own.
- **Caddy** — reverse proxy with automatic ACME (against our step-ca) and a
  simple config format. Terminates TLS for the dashboard, the API, and any
  vhosts you add.
- **Go + Postgres for the management layer** — one static binary, one database.
  RBAC, JWT + API-key auth, audit log, Prometheus metrics.

## Directory layout

```
privatedns/
├── api/               Go management API
├── ca/                step-ca (private CA)
├── dashboard/         Static HTML/JS SPA served by Caddy
├── database/          SQL migrations (pdns + mgmt schemas)
├── dns/
│   ├── authoritative/ PowerDNS Authoritative
│   └── recursor/      Unbound
├── docs/              Full documentation (architecture, ops, security, ...)
├── monitoring/        Prometheus + Grafana
├── reverse-proxy/     Caddy
├── scripts/           install / backup / restore / seed / test-dns
├── tests/             Integration tests
├── docker-compose.yml
├── .env.example
├── Makefile
└── README.md
```

## Quick start

**Prerequisites:** Linux or macOS host with Docker Engine (or Docker Desktop),
Docker Compose plugin, `openssl`, `curl`, and `dig` (for tests).

```bash
git clone <this-repo> privatedns && cd privatedns

# 1) Bootstrap: generates .env with fresh secrets, builds images, starts stack.
./scripts/install.sh

# 2) Seed an example zone so we have something to resolve.
./scripts/seed-example.sh

# 3) Point the current host at our recursor.
# Linux:  edit /etc/resolv.conf (or better, configure via systemd-resolved / NetworkManager)
# macOS:  System Settings → Network → DNS → 127.0.0.1
# Windows: Settings → Network → Change adapter options → Properties → IPv4 → DNS = <host IP>

# 4) Test.
dig @127.0.0.1 www.example.myworld A +short   # -> your DNS_PUBLIC_IP
curl -k https://www.example.myworld/          # -> "Hello from www.example.myworld!"

# 5) (Optional) Trust our private CA so HTTPS works without -k.
./scripts/ca-fetch-root.sh                    # writes privatedns-root.crt
# then install it per the instructions the script prints.
```

The dashboard is at **https://dashboard.myworld/** once you've added
`<DNS_PUBLIC_IP> dashboard.myworld` to your DNS or `/etc/hosts` (or once you're
using the recursor and the bootstrap zone has a `dashboard` A record).

Admin credentials were written to `.env` — see `ADMIN_EMAIL` /
`ADMIN_PASSWORD`. Change the password from the dashboard on first login.

## Configuration

All configuration lives in `.env`. Key knobs:

| Variable            | Default        | Meaning                                                |
|---------------------|----------------|--------------------------------------------------------|
| `PRIVATE_TLD`       | `myworld`      | Your private namespace                                 |
| `BOOTSTRAP_ZONES`   | `example.myworld,home.myworld,lab.myworld` | Zones created on first boot |
| `DNS_PUBLIC_IP`     | `10.10.0.10`   | Host IP baked into NS glue                             |
| `RECURSOR_ACL`      | private CIDRs  | Networks allowed to send queries                       |
| `RECURSOR_UPSTREAMS`| Cloudflare + Quad9 | DoT upstreams for the public Internet              |

See `.env.example` for the full list with inline docs.

## Common operations

```bash
make up          # start
make down        # stop
make ps          # service status
make logs        # tail all logs
make health      # verify DNS + services (runs scripts/healthcheck.sh)
make test-dns    # manual DNS spot-check
make test        # Go unit tests
make backup      # snapshot to ./backups/privatedns-<ts>.tar.gz
make restore ARCHIVE=backups/privatedns-XXXXX.tar.gz
```

### Adding a zone from the CLI

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H content-type:application/json \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" | jq -r .token)

curl -s -X POST http://localhost:8080/api/v1/zones \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"lab.myworld","description":"my homelab"}'

curl -s -X PUT http://localhost:8080/api/v1/zones/lab.myworld/records \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"nas","type":"A","value":"10.10.0.50"}'
```

Prefer long-lived automation? Create an API key from the dashboard's
**API Keys** tab, then use `Authorization: Bearer pd_...` instead of a JWT.

## Documentation

Detailed docs under `docs/`:

- [`architecture.md`](docs/architecture.md) — how the pieces fit together, with diagrams
- [`installation.md`](docs/installation.md) — production install, hardening
- [`security.md`](docs/security.md) — threat model, access control, secrets
- [`operations.md`](docs/operations.md) — day-2 ops, adding zones/servers/vhosts
- [`client-setup.md`](docs/client-setup.md) — configuring Linux/macOS/Windows/iOS/Android/routers
- [`dnssec.md`](docs/dnssec.md) — signing private zones
- [`ha.md`](docs/ha.md) — deploying secondaries + multi-site
- [`disaster-recovery.md`](docs/disaster-recovery.md) — backups, restore drills
- [`troubleshooting.md`](docs/troubleshooting.md) — common failure modes

## Scope and non-goals

- **Not** a global Internet DNS root. The private namespace only resolves for
  clients using this recursor.
- **Not** a registrar or a replacement for public DNS.
- The CA is *internal-use*. Public browsers and mobile apps will not trust it
  until you install the root certificate on those devices.
- If you later buy a public domain (e.g. `example.com`), you can host it on
  this stack too — the code doesn't hard-code any private-TLD assumption
  outside the recursor's stub-zone forward. See `docs/operations.md`.

## License

MIT (see `LICENSE`).

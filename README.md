# privatedns

**Your own private DNS namespace, on your own hardware, in one binary.**

Run `.myworld`, `.lab`, `.internal` — any namespace you want — as your own
authoritative TLD. Resolve `www.example.myworld` from your laptop, phone, or
VPS. Public DNS keeps working: `google.com`, `github.com`, everything else
resolves normally through the upstreams you choose.

Zero SaaS. Zero registrar. Zero recurring cost. One binary that installs as
a service on Windows, Linux, or macOS.

- 🪶 **Single ~5 MB binary.** Pure Go, no runtime dependencies, no CGo.
- 🗄️ **SQLite storage.** One file. `cp` it to back it up.
- 🔒 **Safe defaults.** Never an open recursor. Recursion is loopback-only
  until you explicitly allow more networks.
- 🎨 **Web dashboard included.** Embedded in the binary, no separate build.
- 🔧 **First-class service integration.** `privatedns service install` on
  Windows; systemd + launchd units for Linux and macOS.
- 🛡️ **RBAC + audit log.** Admin / Operator / Viewer roles, JWT + API key auth,
  every mutation logged.
- 🌍 **Cross-platform.** Prebuilt for linux/{amd64,arm64,arm}, windows/{amd64,arm64},
  darwin/{amd64,arm64}. Runs on a Raspberry Pi.

---

## Quick start

### Download a release

Grab the archive for your platform from [releases](https://github.com/shamil3ilm/domain/releases)
(or run `./native/build-release.sh` from a clone — it takes 30 seconds and
needs only Go).

### Linux (systemd)

```bash
tar xzf privatedns_*_linux_amd64.tar.gz
cd privatedns_*_linux_amd64
sudo deploy/linux/install.sh
```

Prints the admin credentials once. Service is running, DNS on port 53, API
on `127.0.0.1:8080`.

### Windows (as a service)

Run PowerShell as Administrator:

```powershell
Expand-Archive privatedns_*_windows_amd64.zip .
cd privatedns_*_windows_amd64
.\privatedns.exe service install
.\privatedns.exe service start
```

### macOS (launchd)

```bash
tar xzf privatedns_*_darwin_arm64.tar.gz
cd privatedns_*_darwin_arm64
sudo deploy/macos/install.sh
```

### From source

```bash
git clone https://github.com/shamil3ilm/domain
cd domain/native
go build -o privatedns .
./privatedns    # foreground; Ctrl-C to stop
```

---

## What you can do

Once it's up, open the dashboard, log in, and:

- **Create a zone.** Any name you like: `example.myworld`, `home.lab`,
  `internal.example.com`. Public and private zones behave the same.
- **Add records.** A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, PTR — the usual.
  The dashboard, the REST API, and API keys all work.
- **Point clients at your server.** LAN clients set DNS to your host's IP;
  clients on the road connect via VPN (Tailscale, WireGuard, etc.).
- **Watch the audit log.** Every mutation, from every actor, forever (or
  until you prune it).

Sample interaction:

```bash
TOKEN=$(curl -s -XPOST http://localhost:8080/api/v1/auth/login \
  -H content-type:application/json \
  -d '{"email":"admin@local","password":"...printed by install..."}' \
  | jq -r .token)

curl -XPOST http://localhost:8080/api/v1/zones \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"lab.myworld","description":"my homelab"}'

curl -XPUT http://localhost:8080/api/v1/zones/lab.myworld/records \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"nas","type":"A","value":"10.10.0.50"}'

dig @<host> nas.lab.myworld  +short
# => 10.10.0.50
```

---

## Configuration

Everything is environment variables. Defaults are safe.

| Variable                          | Default              | Notes                                                     |
|-----------------------------------|----------------------|-----------------------------------------------------------|
| `PRIVATEDNS_DATA_DIR`             | `./data`             | SQLite DB + JWT key live here                             |
| `PRIVATEDNS_PRIVATE_TLD`          | `myworld`            | Bootstrap TLD (you can create as many zones as you want)  |
| `PRIVATEDNS_DNS_ADDR`             | `:53`                | Where the DNS server listens                              |
| `PRIVATEDNS_API_ADDR`             | `:8080`              | Where the HTTP API + dashboard listens                    |
| `PRIVATEDNS_UPSTREAMS`            | `1.1.1.1:53,9.9.9.9:53` | Recursor upstreams                                     |
| `PRIVATEDNS_ADMIN_EMAIL`          | `admin@local`        | Bootstrap admin email                                     |
| `PRIVATEDNS_ADMIN_PASSWORD`       | *generated*          | Bootstrap admin password (printed once)                   |
| `PRIVATEDNS_ALLOW_QUERY_FROM`     | *all*                | CIDRs allowed to query (empty = anyone)                   |
| `PRIVATEDNS_ALLOW_RECURSION_FROM` | `127.0.0.0/8,::1/128`| CIDRs allowed to recurse (empty = loopback only)          |
| `PRIVATEDNS_DNS_RATE_LIMIT_PER_SEC` | `20`               | Per-source-IP query rate (token bucket). `0` disables.    |
| `PRIVATEDNS_DNS_RATE_LIMIT_BURST` | `40`                 | Per-source-IP burst capacity                              |
| `PRIVATEDNS_DNS_RATE_LIMIT_EXEMPT_CIDR` | `127.0.0.0/8,::1/128` | Sources that bypass the rate limiter               |
| `PRIVATEDNS_LOG_LEVEL`            | `info`               | debug / info / warn / error                               |

**Security note:** never set `PRIVATEDNS_ALLOW_RECURSION_FROM` to
`0.0.0.0/0`. That makes you a public open resolver. Set it to your VPN or LAN
subnet — nothing more.

---

## Documentation

- [`docs/deployment-windows.md`](docs/deployment-windows.md) — Windows Service, port 53 quirks
- [`docs/deployment-linux.md`](docs/deployment-linux.md) — systemd on Debian/Ubuntu/RHEL
- [`docs/deployment-macos.md`](docs/deployment-macos.md) — launchd daemon
- [`docs/deployment-vps.md`](docs/deployment-vps.md) — Oracle Free Tier / Hetzner / anywhere
- [`docs/integration-mail.md`](docs/integration-mail.md) — running with `mail-service` for automatic SPF/DKIM/DMARC/MX publishing
- [`docs/security.md`](docs/security.md) — threat model, ACLs, hardening
- [`docs/client-setup.md`](docs/client-setup.md) — pointing clients at your DNS
- [`docs/operations.md`](docs/operations.md) — day-2 tasks, adding zones/records via API
- [`native/README.md`](native/README.md) — native single-binary details
- [`api/openapi.yaml`](api/openapi.yaml) — REST API reference

## Docker variant

An optional feature-complete Docker Compose stack lives at the repo root
(`docker-compose.yml`, `dns/`, `api/`, `ca/`, `reverse-proxy/`, `database/`,
`monitoring/`). It adds:

- PowerDNS Auth + Unbound + PostgreSQL (instead of the single Go binary)
- Full DNSSEC key management
- Automatic HTTPS via a bundled step-ca and Caddy ACME
- Prometheus + Grafana observability

Use it if you want DNSSEC, ACME, or plan to run secondary DNS servers.
Everything else — the native binary — is easier.

---

## Cost

Free. All open-source components (Go, `miekg/dns`, `modernc.org/sqlite`,
`chi`, `golang-jwt`, `x/crypto`, `x/time`). No registrar for private
namespaces. No CA fees. Deploy on hardware you already own or Oracle's
always-free VPS tier — either works.

## License

MIT. See [`LICENSE`](LICENSE).

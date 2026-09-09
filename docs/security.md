# Security

## Threat model

We assume:

- Any client on your VPN/LAN is trusted to *query* the recursor but is not
  trusted to modify DNS records.
- The compose host itself is trusted (root on the host owns everything).
- Backups may be exposed on shared storage; they contain secrets and must be
  handled accordingly.

We defend against:

- Unauthorized DNS mutation from the LAN (API auth + RBAC).
- Cache-poisoning against the recursor (DNSSEC validation of upstream +
  DoT + query minimisation + 0x20 case randomization).
- Open-recursor abuse (ACL-gated on `RECURSOR_ACL`).
- Zone-transfer scraping (`allow-axfr-ips` restricted to compose subnet).
- API brute-force (rate limit per IP/user; bcrypt with cost 12).
- Long-lived compromised tokens (12h JWT expiry + revocation list; API-key
  hashes stored as SHA-256, never plaintext).
- Common web attacks (HSTS, XFO=DENY, XCTO=nosniff, strict CSP on dashboard).

We do **not** defend against:

- A compromised compose host (that's game over — the host has all secrets).
- A malicious admin (RBAC helps but admins can escalate).
- Physical access to the machine.

## Access control

Three roles:

| Role       | Can read      | Can mutate     | Can manage users |
|------------|---------------|----------------|------------------|
| `admin`    | everything    | everything     | yes              |
| `operator` | everything    | zones, records | no               |
| `viewer`   | everything    | nothing        | no               |

Every user can manage their own API keys.

Roles are stored on the user; JWTs carry the role in a signed claim. API keys
are hashed with SHA-256 (they carry 240 bits of entropy — bcrypt is overkill
and too slow for a hot API path).

## Secret management

Secrets live in `.env` (mode 600 recommended) and are passed to containers as
environment variables. No secret is baked into an image.

Rotation:

- `POSTGRES_PASSWORD` — change in `.env`, then `ALTER USER ... PASSWORD ...`
  in the DB. Restart the stack.
- `PDNS_API_KEY` — change in `.env`, restart `pdns-auth` and `api`.
- `API_JWT_SECRET` — change in `.env`, restart `api`. All existing JWTs
  become invalid (users have to log in again).
- `CA_PASSWORD` — see step-ca docs; involves re-encrypting the CA key.
- `ADMIN_PASSWORD` — change from the dashboard.

## Audit log

Every mutating action writes to `mgmt.audit_log` with:

- timestamp (UTC, from Postgres),
- acting user (or API key) and IP,
- action + target,
- outcome (success / failure / denied),
- structured detail JSON.

The log is append-only from the application's perspective. To satisfy stricter
requirements you can revoke DELETE from the API's Postgres role or ship the
log to an external system.

## Network exposure

| Port on host  | Service        | Public? | Notes                                    |
|---------------|----------------|---------|------------------------------------------|
| 53 UDP/TCP    | recursor       | LAN     | Restrict to VPN/LAN in firewall          |
| 5353 UDP/TCP  | pdns-auth      | LAN     | Only needed for external secondaries     |
| 80 / 443      | reverse-proxy  | LAN     | HTTP is used for ACME HTTP-01 challenges |
| 9000          | ca             | LAN     | ACME + admin endpoint                    |
| 9090          | prometheus     | admin   | Restrict via firewall                    |
| 3000          | grafana        | admin   | Restrict via firewall                    |

Inside the compose network everything is reachable. Postgres, the API, and
PowerDNS's HTTP API are **never** exposed to the host.

## HTTPS trust

The private CA is not trusted by clients out of the box. Options:

1. **Install the CA root** on each device (see `docs/client-setup.md`). This
   is the recommended approach for LAN/VPN-only deployments.
2. **Use a public CA** by pointing Caddy at a real ACME server. This only
   works for names in a domain you actually control publicly, so it doesn't
   apply to `.myworld`.
3. **Accept warnings** for personal use. Fine for the dashboard but painful
   for anything else.

## DNSSEC posture

The recursor validates DNSSEC for the public Internet. It **does not**
validate DNSSEC for the private TLD because there's no publicly-anchored trust
chain for a made-up TLD like `.myworld`.

If you want DNSSEC for internal zones, generate a KSK for the private root and
distribute the DS record to your clients as a `trust-anchor`. See
`docs/dnssec.md`.

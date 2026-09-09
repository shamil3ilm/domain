# Disaster recovery

## What we back up

`scripts/backup.sh` produces `backups/privatedns-<timestamp>.tar.gz` containing:

| Component        | Source                        | Notes                        |
|------------------|-------------------------------|------------------------------|
| Postgres dump    | `pg_dump -Fc`                 | Both `pdns` and `mgmt` schemas — this is the source of truth for zones, records, users, keys, audit, DNSSEC keys |
| CA state         | `ca_data` volume              | Root + intermediate keys, ACME provisioners, ca.json |
| Caddy state      | `caddy_data` volume           | Issued certs; regeneratable but restoring saves re-enrollment |
| `.env`           | file                          | All configuration + secrets  |
| `docker-compose.yml` | file                      | (usually version-controlled elsewhere too) |

DNSSEC keys are stored in the `pdns.cryptokeys` table — the Postgres dump
captures them. **Losing the Postgres dump means losing DNSSEC keys**, which
means every recursor with your trust anchor cached will SERVFAIL until the new
keys propagate.

## Backup cadence

Recommended cron:

```
# Daily, 03:00 local
0 3 * * *  cd /opt/privatedns && ./scripts/backup.sh >> /var/log/privatedns-backup.log 2>&1

# Prune older than 30 days (adjust to policy)
0 4 * * *  find /opt/privatedns/backups -name 'privatedns-*.tar.gz' -mtime +30 -delete
```

## Off-site copy

Backups contain secrets. Encrypt before shipping:

```bash
LATEST=$(ls -1t backups/privatedns-*.tar.gz | head -n1)
gpg --symmetric --cipher-algo AES256 "$LATEST"
rclone copy "$LATEST.gpg" remote:privatedns-backups/
```

Passphrase or key must be stored somewhere **not** in the backup itself.

## Restore drill (do this at least once)

On a fresh host:

```bash
git clone <repo> privatedns && cd privatedns
./scripts/restore.sh /path/to/privatedns-<ts>.tar.gz
```

The script:

1. Extracts the archive.
2. Copies `.env` back into place.
3. Drops and recreates Postgres volumes, then `pg_restore`s.
4. Drops and recreates the CA and Caddy volumes, then extracts them.
5. Starts the stack.

After restore, verify with:

```bash
make health
./tests/integration/test_dns.sh
```

## Recovery scenarios

### "I nuked the Postgres volume"

`./scripts/restore.sh <latest>` — everything's in the dump.

### "The host died"

New host, git clone, restore. IP changes are transparent as long as
`DNS_PUBLIC_IP` in `.env` is updated and the NS glue in the root zone
points at the new IP.

### "I lost the CA private keys"

You'll need to re-issue every certificate.

Workaround: after `docker compose up -d`, every Caddy-served vhost will
re-enroll via ACME automatically the next time it needs a cert (which happens
within a few seconds of a request). Clients that trusted the *old* root will
need the *new* root distributed — see `docs/client-setup.md`.

To avoid this: back up regularly and off-site.

### "I lost DNSSEC keys and I'd already published DS"

If you signed a public parent's DS to point at these keys and lost them, you
have a hard problem: every DNSSEC-validating resolver on the Internet will
SERVFAIL your zone until DS TTL expires *and* the new DS is published.

Mitigation is prevention. See `docs/dnssec.md` on why we don't recommend DNSSEC
for pure private setups.

## Verification schedule

Run the restore drill quarterly on a spare host or a temporary machine. A
backup that hasn't been restored is only theoretically a backup.

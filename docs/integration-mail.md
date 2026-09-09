# Integrating privatedns with mail-service

End-to-end walkthrough for running **privatedns** (this repo) and
[**mail-service**](https://github.com/shamil3ilm/mail-service) side-by-side
on one VPS, with automatic SPF/DKIM/DMARC/MX publishing over the privatedns
REST API.

The intended deployment model is **Path 2**: a mail domain is a subdomain
of an existing domain (or a freshly registered domain) delegated to the
VPS's authoritative nameserver.

> **mail-service status note.** mail-service is in early phases. The DNS
> publisher hooks (`MAIL_DNS_PUBLISHER=privatedns`) are documented in its
> `.env.example` today, but full end-to-end integration lands in
> subsequent phases. Configuring the bridge now means it works as soon as
> mail-service ships the publisher — no reconfiguration required.

---

## 1. Prerequisites

| Requirement                    | Why                                          |
|--------------------------------|----------------------------------------------|
| A registered domain            | To delegate at your registrar                |
| A VPS with a public static IP  | Required for MX + authoritative DNS          |
| `25/tcp` outbound + inbound    | SMTP delivery                                |
| `53/tcp` and `53/udp` inbound  | Authoritative DNS                            |
| `443/tcp` inbound              | Dashboards (via HTTPS reverse proxy)         |
| `587/tcp` inbound              | Authenticated submission (cloud mode)        |
| Registrar access               | To create glue + NS records                  |
| Go 1.23+ on a build host       | To cross-compile if not using releases       |

**Check your VPS provider's outbound-SMTP policy.** Many providers block
`25/tcp` outbound by default (AWS, GCP, Oracle Free Tier's basic plan,
DigitalOcean until you request removal). Some require a support ticket to
unblock. Without outbound 25, you can *receive* mail but not *send* it
directly — you'd need to relay through a third party
(`MAIL_RELAY_PROVIDER=smtp` or `resend`).

Verify before proceeding:

```bash
# From the VPS:
nc -vz smtp.gmail.com 25
```

---

## 2. Install privatedns

Follow the standard Linux install:

```bash
tar xzf privatedns_*_linux_amd64.tar.gz
cd privatedns_*_linux_*
sudo deploy/linux/install.sh \
  --tld example.com \
  --api-addr 127.0.0.1:8080
```

Do *not* set `--allow-recursion` — the bridge and mail-service only need
authoritative answers, and the default (loopback-only recursion) is safe.
See [`deployment-linux.md`](deployment-linux.md) for firewall setup and
port-53 conflict resolution.

Save the printed admin password — mail-service will need it to
authenticate to the privatedns API.

Verify:

```bash
systemctl status privatedns
dig @127.0.0.1 example.com SOA +short
```

---

## 3. Install mail-service

mail-service does not currently ship a one-shot Linux installer. The
supported install steps are:

```bash
# Build the binary (on a host with Go 1.24+)
git clone https://github.com/shamil3ilm/mail-service /tmp/mail-service
cd /tmp/mail-service
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/mailservice ./cmd/mailservice

# Copy to the target host + install
scp /tmp/mailservice vps:/tmp/
ssh vps
sudo install -m 0755 /tmp/mailservice /usr/local/bin/mailservice
sudo useradd --system --no-create-home --shell /usr/sbin/nologin mailservice
sudo install -d -m 0750 -o mailservice -g mailservice /var/lib/mailservice
sudo install -d -m 0750 -o root -g mailservice /etc/mailservice

# Install the systemd unit shipped in mail-service's repo.
sudo curl -o /etc/systemd/system/mailservice.service \
  https://raw.githubusercontent.com/shamil3ilm/mail-service/main/deploy/systemd/mailservice.service
sudo systemctl daemon-reload
```

Alternatively, use the [combined installer](../native/deploy/linux/install-with-mail.sh)
which does all of this in one step. See [§8](#8-one-shot-combined-install).

Do **not** start the service yet — we still need to write the env file.

---

## 4. Configure the DNS bridge

Create `/etc/mailservice/env` with the following minimum configuration:

```env
# Deployment mode (public MX + relay)
MAIL_MODE=cloud
MAIL_LISTEN_ADDR=0.0.0.0

# Storage
MAIL_DB_PATH=/var/lib/mailservice/mail.db
MAIL_RAW_STORE_PATH=/var/lib/mailservice/raw

# HTTP dashboard (loopback — expose via reverse proxy or SSH tunnel)
MAIL_HTTP_PORT=8035
MAIL_ADMIN_PORT=8036

# Public SMTP
MAIL_SMTP_PORT=25
MAIL_SUBMISSION_PORT=587

# Auto-verify local suffixes only (production leaves this to DNS)
MAIL_AUTO_VERIFY_DOMAINS=.test,.local,.localhost

# ---- DNS bridge --------------------------------------------------------
# Talks to privatedns on the same box over loopback. No TLS needed.
MAIL_DNS_PUBLISHER=privatedns
MAIL_DNS_PUBLISHER_URL=http://127.0.0.1:8080
MAIL_DNS_PUBLISHER_USER=admin@local
MAIL_DNS_PUBLISHER_TOKEN=<paste-privatedns-admin-password-or-api-key>
```

Permissions:

```bash
sudo chown root:mailservice /etc/mailservice/env
sudo chmod 0640 /etc/mailservice/env
```

Never commit the token to source control. Rotating the token is a matter
of editing this file and running `systemctl restart mailservice`.

**Recommended:** create a dedicated API key for the bridge instead of using
the admin password. From the privatedns dashboard: **API Keys → New key**.
Copy the token (shown once) into `MAIL_DNS_PUBLISHER_TOKEN`. Revoking the
key later is a single click; rotating the admin password is more disruptive.

Start mail-service:

```bash
sudo systemctl enable --now mailservice
sudo systemctl status mailservice
```

---

## 5. DNS delegation

For **Path 2**, you're delegating either an existing subdomain or a fresh
domain to the VPS.

**Case A — existing domain, mail subdomain (`mail.example.com`):**

1. In your registrar / DNS host for `example.com`, add:
   ```
   ns1.example.com         A    <VPS-public-IP>
   mail.example.com        NS   ns1.example.com
   ```
2. Wait for propagation (`dig +trace mail.example.com NS`).
3. In privatedns, the zone `mail.example.com` is now authoritative on
   your VPS. Create it:
   ```
   curl -X POST http://127.0.0.1:8080/api/v1/zones \
     -H "authorization: bearer $TOKEN" -H content-type:application/json \
     -d '{"name":"mail.example.com"}'
   ```
4. Add the mail host itself:
   ```
   curl -X PUT http://127.0.0.1:8080/api/v1/zones/mail.example.com/records \
     -H "authorization: bearer $TOKEN" -H content-type:application/json \
     -d '{"name":"@","type":"A","value":"<VPS-public-IP>"}'
   ```

**Case B — fresh domain delegated entirely (`mail.example.net`):**

Same shape, but at the *root* domain level:

```
ns1.example.net        A    <VPS-public-IP>       (glue at registrar)
example.net            NS   ns1.example.net       (delegation at registrar)
```

Some registrars require **glue records** to be registered at the parent
zone level (a separate UI panel). Without glue, the recursive resolver
can't find your NS. Every registrar UI is different — search their docs
for "glue record" or "child nameserver."

The mail-service DNS bridge will populate MX/SPF/DKIM/DMARC records
inside the zone automatically when you create a mail domain via its API.
For now, if you want to seed them manually, use the batch endpoint:

```bash
curl -X PUT http://127.0.0.1:8080/api/v1/zones/mail.example.com/records:batch \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{
    "records": [
      {"name":"@",     "type":"MX",  "ttl":3600, "value":"10 mail.example.com."},
      {"name":"@",     "type":"TXT", "value":"v=spf1 mx -all"},
      {"name":"_dmarc","type":"TXT", "value":"v=DMARC1; p=reject; rua=mailto:postmaster@example.com"}
    ]
  }'
```

(DKIM records require a keypair that mail-service will generate; add the
`_domainkey.<selector>` TXT record after mail-service publishes the
public key.)

---

## 6. Verify with dig

Run each of these from a machine **outside** your VPS to prove real
resolution:

```bash
# Delegation is live?
dig +trace mail.example.com NS

# Authoritative answers?
dig @<VPS-IP> mail.example.com SOA +short
dig @<VPS-IP> mail.example.com MX  +short

# SPF / DMARC records?
dig @<VPS-IP> mail.example.com     TXT +short
dig @<VPS-IP> _dmarc.mail.example.com TXT +short

# DKIM (once mail-service has published a key)?
dig @<VPS-IP> selector1._domainkey.mail.example.com TXT +short
```

DNS caching means the wider Internet may not see your records for up to
the TTL you set. Use `+trace` and query authoritative servers directly to
sidestep caches.

---

## 7. Test mail

The full round-trip once mail-service's message routing lands:

1. Create a mail domain via mail-service's API/dashboard.
2. mail-service asks privatedns to publish MX/SPF/DMARC/DKIM records.
3. Verify via `dig` (§6).
4. Create a mailbox.
5. Send outbound mail from the mailbox.
6. Receive inbound mail via port 25.
7. On failure, check `journalctl -u mailservice -f` and
   `journalctl -u privatedns -f` side by side.

Until mail-service reaches that phase, use the batch endpoint (§5) to
seed the records manually, and test the DNS side end-to-end with public
tools like [mail-tester.com](https://www.mail-tester.com/).

---

## 8. One-shot combined install

If you prefer a scripted install of both services on a fresh
Debian/Ubuntu VPS:

```bash
tar xzf privatedns_*_linux_amd64.tar.gz
cd privatedns_*_linux_*
sudo deploy/linux/install-with-mail.sh \
  --mail-binary /path/to/mailservice \
  --domain mail.example.com
```

See [`../native/deploy/linux/install-with-mail.sh`](../native/deploy/linux/install-with-mail.sh)
for details.

---

## 9. Security notes

- **Never expose the privatedns HTTP API publicly.** Bind it to
  `127.0.0.1` (the default). mail-service talks to it over loopback. The
  dashboard reaches you over an SSH tunnel or a reverse proxy with real
  TLS and authentication.
- **Use an API key, not the admin password**, for the bridge. Rotating a
  key is one dashboard click.
- **Keep the OS firewall enabled.** `ufw` or `nftables` rules should
  restrict `53`, `25`, `587` to the sources you actually expect, or at
  minimum apply generic rate limits on top of the built-in per-source
  DNS rate limiter (see [`deployment-vps.md`](deployment-vps.md)).
- **Rate limiting is now built into privatedns** (`PRIVATEDNS_DNS_RATE_LIMIT_*`).
  External `nftables` remains valuable as defence-in-depth.
- **Set restrictive perms on secrets:** `/etc/mailservice/env` should be
  `0640 root:mailservice`. Never commit it.
- **DMARC starts strict, relaxes if needed.** Deploy `p=quarantine` for
  the first few weeks, monitor the `rua` reports, then move to
  `p=reject` once you're sure legitimate mail is passing.
- **Auto-verify only local suffixes.** Keep `MAIL_AUTO_VERIFY_DOMAINS`
  scoped to `.test/.local/.localhost` in production; real domains must
  pass real DNS verification.

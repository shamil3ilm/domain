# Deploying privatedns on Linux

Tested on Ubuntu 22.04/24.04, Debian 12, Fedora 39+, Alpine 3.20. Any distro
with systemd works.

## 1. Prerequisites

- systemd (any recent distro)
- A network interface with a stable address you'll point clients at
- Port 53 free — on Ubuntu, `systemd-resolved` binds it by default. Disable
  its stub listener (see below).

## 2. Install

Grab the release archive for your architecture (amd64, arm64, arm) and run
the installer:

```bash
tar xzf privatedns_*_linux_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz
cd privatedns_*_linux_*
sudo deploy/linux/install.sh
```

The script:
1. Creates a system user `privatedns`.
2. Installs the binary to `/usr/local/bin/privatedns`.
3. Creates `/etc/privatedns/privatedns.env` with a generated admin password.
4. Installs a hardened systemd unit at `/etc/systemd/system/privatedns.service`.
5. Enables and starts the service.
6. Prints the bootstrap admin credentials once.

Options:

```
sudo deploy/linux/install.sh \
  --tld company.internal \
  --allow-recursion 10.10.0.0/16 \
  --api-addr 0.0.0.0:8080
```

## 3. Fix port 53 conflict (systemd-resolved)

If the installer warned about port 53 already being in use:

```bash
sudo sed -i 's/^#\?DNSStubListener=.*/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo systemctl restart systemd-resolved
sudo systemctl restart privatedns
```

`systemd-resolved` still resolves DNS for the host (for services that consume
its D-Bus API); it just doesn't own the port 53 socket anymore.

## 4. Firewall

Only expose port 53 to networks that should query you.

**UFW (Ubuntu/Debian):**

```bash
sudo ufw allow from 10.10.0.0/16 to any port 53 proto udp
sudo ufw allow from 10.10.0.0/16 to any port 53 proto tcp
sudo ufw allow from 10.10.0.0/16 to any port 8080 proto tcp  # dashboard
```

**firewalld (RHEL/Fedora):**

```bash
sudo firewall-cmd --permanent --new-zone=privatedns
sudo firewall-cmd --permanent --zone=privatedns --add-source=10.10.0.0/16
sudo firewall-cmd --permanent --zone=privatedns --add-port=53/udp
sudo firewall-cmd --permanent --zone=privatedns --add-port=53/tcp
sudo firewall-cmd --permanent --zone=privatedns --add-port=8080/tcp
sudo firewall-cmd --reload
```

**nftables:**

```
table inet privatedns {
  set trusted { type ipv4_addr; flags interval; elements = { 10.10.0.0/16 } }
  chain input {
    type filter hook input priority 0; policy accept;
    ip saddr @trusted udp dport 53 accept
    ip saddr @trusted tcp dport 53 accept
    ip saddr @trusted tcp dport 8080 accept
  }
}
```

## 5. Verify

```bash
systemctl status privatedns
sudo journalctl -u privatedns -n 50 --no-pager

# Query the server locally.
dig @127.0.0.1 www.example.myworld +short   # empty until you create records
dig @127.0.0.1 google.com +short             # public still works

# Log in and create your first record.
source /etc/privatedns/privatedns.env
TOKEN=$(curl -sX POST http://localhost:8080/api/v1/auth/login \
  -H content-type:application/json \
  -d "{\"email\":\"$PRIVATEDNS_ADMIN_EMAIL\",\"password\":\"$PRIVATEDNS_ADMIN_PASSWORD\"}" \
  | jq -r .token)

curl -sX POST http://localhost:8080/api/v1/zones \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"example.myworld"}'

curl -sX PUT http://localhost:8080/api/v1/zones/example.myworld/records \
  -H "authorization: bearer $TOKEN" -H content-type:application/json \
  -d '{"name":"www","type":"A","value":"10.10.0.50"}'

dig @127.0.0.1 www.example.myworld +short
# => 10.10.0.50
```

## 6. Enable recursion for LAN clients (optional)

By default the recursor answers only for `127.0.0.1`. To let LAN clients use
this box as their DNS, edit `/etc/privatedns/privatedns.env`:

```
PRIVATEDNS_ALLOW_RECURSION_FROM=10.10.0.0/16
```

Then:

```
sudo systemctl restart privatedns
```

**Do not** set this to `0.0.0.0/0` on a public-facing VPS — that makes you a
DDoS amplifier.

## 7. Backups

```bash
sudo cp /var/lib/privatedns/privatedns.db /var/backups/privatedns-$(date -u +%Y%m%dT%H%M%SZ).db
```

Restore by stopping the service, copying the file back, and starting again.
Add this to cron.

## Uninstall

```bash
sudo deploy/linux/uninstall.sh          # keep data
sudo deploy/linux/uninstall.sh --purge  # wipe everything
```

## Troubleshooting

- **Service fails immediately:** `journalctl -u privatedns -n 100 --no-pager`.
- **Port 53 bind error:** something else has port 53 (usually
  `systemd-resolved` or another DNS server). See §3.
- **Dashboard unreachable:** the default `PRIVATEDNS_API_ADDR` is
  `127.0.0.1:8080` — use an SSH tunnel (`ssh -L 8080:localhost:8080 host`) or
  change it to `0.0.0.0:8080` and firewall accordingly.

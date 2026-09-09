# Deploying privatedns on a VPS

Any always-on Linux server works. This walkthrough covers the two shapes
that make sense for privatedns:

- **Shape A — VPN-only, safe by default.** VPS is only reachable from your
  VPN (Tailscale/WireGuard). Everything works. Recommended for personal use.
- **Shape B — public authoritative.** VPS answers real DNS queries on port
  53 from the public Internet, but only for zones you've defined.
  Recursion is refused for external clients. Suitable if you plan to
  delegate a real domain to the box later.

## Free options

- **Oracle Cloud Always Free.** 24 GB ARM Ampere VM, real public IP, free
  forever. Requires a credit card for identity verification only. This is
  the best free-tier VPS in 2026.
- **Google Cloud e2-micro (US regions only).** Always-free tier, small.
- **Hetzner CX11.** ~€4/mo. Not free but the cheapest reliable option.

## Shape A — VPN-only deployment (recommended)

Zero public exposure. All clients — laptops, phones, other servers — reach
the DNS server via the VPN mesh.

### 1. Provision the VPS

Any Ubuntu 22.04/24.04 image. `ssh` in.

### 2. Install Tailscale

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
```

Note the tailnet IP the VPS gets (e.g. `100.64.0.5`).

### 3. Install privatedns

Copy the release archive up, then:

```bash
tar xzf privatedns_*_linux_amd64.tar.gz
cd privatedns_*_linux_amd64

sudo deploy/linux/install.sh \
  --tld myworld \
  --allow-recursion 100.64.0.0/10 \
  --dns-addr "100.64.0.5:53"
```

`100.64.0.0/10` is the Tailscale CGNAT range. Adjust for WireGuard or your
tailnet's specific subnet if you've configured one.

Binding `PRIVATEDNS_DNS_ADDR` to the Tailscale interface's IP means the DNS
server never accepts a packet from the public Internet — even if the
firewall is misconfigured, the OS won't route it to us.

### 4. Configure Tailscale Split DNS

In the Tailscale admin console → DNS → Nameservers:

- Add nameserver `100.64.0.5` (your VPS's tailnet IP)
- Restrict to domain `myworld`
- Save

Every device on your tailnet now resolves `*.myworld` via your VPS and
everything else via its normal DNS. Same setup works with headscale (the
self-hosted Tailscale coordinator) if you prefer.

### 5. Firewall

Block port 53 from the public Internet entirely:

```bash
sudo ufw allow in on tailscale0 to any port 53
sudo ufw deny  in            to any port 53
sudo ufw allow ssh
sudo ufw enable
```

### 6. Verify from a laptop on the tailnet

```bash
tailscale status
dig @100.64.0.5 www.example.myworld +short
```

## Shape B — public authoritative deployment

Only use this if you plan to point real (Internet) traffic at the box.

### 1. Provision + install privatedns

```bash
sudo deploy/linux/install.sh \
  --tld example.com \
  --api-addr 127.0.0.1:8080    # dashboard stays on loopback
# leave --allow-recursion unset — recursion is refused for outside clients
```

### 2. Delegate at your registrar

At your registrar (Cloudflare, Porkbun, Namecheap…):

- Point `example.com` at your VPS. Two options:
  - Register a **glue record**: `ns1.example.com IN A <VPS-IP>` (needs
    registrar support), then set `example.com` NS to `ns1.example.com`.
  - Or use two other nameserver hostnames you control (e.g.
    `ns1.something.else`) and delegate `example.com` NS to them, with those
    hostnames' A records pointing at the VPS.
- Propagation takes a few hours.

### 3. Firewall

Open 53 to the world, keep everything else restricted:

```bash
sudo ufw allow 53/udp
sudo ufw allow 53/tcp
sudo ufw allow ssh
sudo ufw allow from 100.64.0.0/10 to any port 8080  # dashboard via tailscale
sudo ufw enable
```

**Test that recursion is refused externally** before considering the setup
done:

```bash
# From a machine outside your VPN:
dig @<vps-ip> google.com
# Expect: status: REFUSED
```

If you see an answer, do not proceed — your firewall or config is wrong.
Open resolvers are abused for DDoS amplification and will get you nullrouted
by your provider.

### 4. Rate limiting

**privatedns has built-in per-source-IP token-bucket rate limiting.** No
external firewall rules are required for basic DNS query rate limiting.
Configuration via environment variables:

| Variable                                | Default              | Meaning                                      |
|-----------------------------------------|----------------------|----------------------------------------------|
| `PRIVATEDNS_DNS_RATE_LIMIT_PER_SEC`     | `20`                 | Sustained query rate per source IP           |
| `PRIVATEDNS_DNS_RATE_LIMIT_BURST`       | `40`                 | Burst capacity per source IP                 |
| `PRIVATEDNS_DNS_RATE_LIMIT_EXEMPT_CIDR` | `127.0.0.0/8,::1/128`| Sources that bypass the limiter entirely     |

Over-limit queries are silently dropped (no response) — the correct
behavior for an authoritative server exposed to the Internet, since
error responses would themselves be amplification vectors. The drop
counter is available in debug logs.

Tune `PRIVATEDNS_DNS_RATE_LIMIT_PER_SEC` to your expected legitimate
traffic. For heavy production loads, set higher and add nftables as a
defence-in-depth layer:

```
table inet dns {
  set client_rate { type ipv4_addr; flags dynamic, timeout; timeout 1m; size 65535 }
  chain input {
    type filter hook input priority filter; policy accept;
    udp dport 53 update @client_rate { ip saddr limit rate over 100/second } drop
  }
}
```

Set the nftables threshold *above* the in-app limit so the in-app limiter
handles per-IP fairness and nftables catches broader abuse patterns.

### 5. Add a secondary (recommended)

Real Internet DNS should have redundancy. See [`ha.md`](ha.md) for
setting up a second privatedns instance that AXFRs zones from the primary.
(Note: the native binary doesn't yet support AXFR — use the Docker version
under `../` if you need this, or run privatedns as one authoritative and a
second dedicated authoritative from another software package.)

## Cost

Both shapes deployed on Oracle Free Tier: **$0/mo forever** if the free tier
persists. Tailscale free tier: **$0** for up to 3 users and 100 devices.

## Choosing between the shapes

- **Just for you or a small team?** Shape A. Zero blast radius, no rate
  limiting worries, phones "just work" on cellular via Tailscale.
- **Hosting a real domain long-term?** Shape B. Requires more discipline
  (firewall, rate limits, secondary), but supports normal registrar
  delegation.
- **Both?** Point real DNS at the VPS (Shape B) *and* run Tailscale on the
  VPS. Your tailnet devices talk to it over CGNAT; the world talks to it
  over the public IP.

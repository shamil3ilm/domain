# Client setup

Two things to configure on each device that should use the private namespace:

1. **DNS resolver** — point at your recursor.
2. **CA root certificate** — install so HTTPS to `*.myworld` is trusted.

## 1. DNS resolver

Assume the recursor is reachable at `10.10.0.10` (whatever your
`DNS_PUBLIC_IP` is).

### Linux (systemd-resolved)

Edit `/etc/systemd/resolved.conf`:

```
[Resolve]
DNS=10.10.0.10
FallbackDNS=1.1.1.1
Domains=~myworld
```

Then `sudo systemctl restart systemd-resolved`.

### Linux (NetworkManager)

Per-connection, no config file:

```
nmcli connection modify "<Wi-Fi SSID>" ipv4.dns "10.10.0.10 1.1.1.1"
nmcli connection modify "<Wi-Fi SSID>" ipv4.ignore-auto-dns yes
nmcli connection up   "<Wi-Fi SSID>"
```

### macOS

`System Settings → Network → your interface → Details → DNS → +` add
`10.10.0.10` as the first entry.

Or via CLI (`en0` is Wi-Fi on most Macs):

```
sudo networksetup -setdnsservers Wi-Fi 10.10.0.10 1.1.1.1
```

### Windows

`Settings → Network & Internet → your adapter → DNS server assignment → Manual`.
Preferred DNS = `10.10.0.10`.

Or PowerShell:

```powershell
Set-DnsClientServerAddress -InterfaceAlias "Wi-Fi" -ServerAddresses ("10.10.0.10","1.1.1.1")
```

### Android

`Settings → Network & Internet → Private DNS → Off` (Private DNS forces
DoT/DoH which won't hit our recursor). Then set the DNS per Wi-Fi network in
the network's advanced settings.

Or use an app like Nebula/Tailscale/WireGuard and configure DNS at the tunnel
level — this is the recommended approach for mobile.

### iOS

DNS is configured per Wi-Fi network under Settings → Wi-Fi → (i) → Configure
DNS → Manual.

For system-wide DNS control on cellular data, use a Configuration Profile
(DNS Settings payload) — search "iOS DNS configuration profile" for tools.

### Router (recommended)

If you set the router's LAN DHCP DNS to your recursor, every device on the
network gets it automatically. This is by far the easiest approach for home
labs.

## 2. Install the CA root certificate

```bash
./scripts/ca-fetch-root.sh                    # writes privatedns-root.crt
```

The script prints installation commands. Summarized:

### Linux (Debian/Ubuntu)

```bash
sudo cp privatedns-root.crt /usr/local/share/ca-certificates/
sudo update-ca-certificates
```

### Linux (Fedora/RHEL)

```bash
sudo cp privatedns-root.crt /etc/pki/ca-trust/source/anchors/
sudo update-ca-trust
```

### macOS

```bash
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain privatedns-root.crt
```

### Windows (as Administrator)

```powershell
Import-Certificate -FilePath .\privatedns-root.crt `
  -CertStoreLocation Cert:\LocalMachine\Root
```

### Android

Copy the file to the device. Settings → Security → Encryption & credentials →
Install a certificate → CA certificate → pick the file.

**Important:** Android 7+ ignores user-installed CAs for most apps unless
the app opts in via a network security config. Chrome respects them; many
apps do not. This is by design and outside our control.

### iOS

AirDrop or email the `.crt` to yourself, tap to open, then Settings →
General → VPN & Device Management → install the profile. Then Settings →
General → About → Certificate Trust Settings → enable full trust for the
privatedns root.

### Browsers (Firefox)

Firefox maintains its own trust store on Windows/macOS/Linux. Settings →
Privacy & Security → Certificates → View Certificates → Authorities → Import.

## 3. Test

```bash
dig  @<recursor-ip> www.example.myworld A +short   # -> your DNS_PUBLIC_IP
curl https://www.example.myworld/                  # should NOT need -k
```

If curl succeeds without `-k`, TLS trust is working.

If `dig` returns nothing but `dig @<recursor-ip> google.com` works, the
recursor is up but the private zone is empty. Run `scripts/seed-example.sh`
from the host running the stack.

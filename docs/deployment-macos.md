# Deploying privatedns on macOS

Runs as a launchd system daemon. Works on both Intel and Apple Silicon.

## 1. Prerequisites

- macOS 12+ (Monterey or later; earlier versions probably work)
- Administrator access

## 2. Install

```bash
tar xzf privatedns_*_darwin_arm64.tar.gz   # or _amd64 on Intel Macs
cd privatedns_*_darwin_*
sudo deploy/macos/install.sh
```

The installer:
1. Copies the binary to `/usr/local/bin/privatedns`.
2. Creates `/etc/privatedns/privatedns.env` with a generated admin password.
3. Installs a wrapper script that sources the env file (launchd has no
   equivalent of systemd's `EnvironmentFile=`).
4. Loads `/Library/LaunchDaemons/com.privatedns.plist`.
5. Prints the admin credentials once.

## 3. Manage the daemon

```bash
sudo launchctl unload /Library/LaunchDaemons/com.privatedns.plist   # stop
sudo launchctl load   /Library/LaunchDaemons/com.privatedns.plist   # start
sudo launchctl list | grep privatedns                                # status
```

Logs:

```
/usr/local/var/log/privatedns.log
```

## 4. Firewall

macOS's built-in Application Firewall isn't ideal for port-based rules. Use
`pfctl` if you need real firewall rules:

```
# /etc/pf.anchors/privatedns
block in proto {tcp udp} from any to any port 53
pass  in proto {tcp udp} from 10.10.0.0/16 to any port 53
```

Then enable:

```bash
sudo pfctl -e -f /etc/pf.conf
```

For a simple home Mac used as the family DNS, this is overkill — just don't
expose the Mac's public IP.

## 5. Verify

```bash
dig @127.0.0.1 www.example.myworld +short
dig @127.0.0.1 google.com +short

source /etc/privatedns/privatedns.env
TOKEN=$(curl -sX POST http://localhost:8080/api/v1/auth/login \
  -H content-type:application/json \
  -d "{\"email\":\"$PRIVATEDNS_ADMIN_EMAIL\",\"password\":\"$PRIVATEDNS_ADMIN_PASSWORD\"}" \
  | jq -r .token)

open http://localhost:8080/
```

## 6. Point the Mac itself at the daemon

`System Settings → Network → <interface> → Details → DNS`. Add `127.0.0.1`
as the primary DNS server, keep `1.1.1.1` as a fallback.

Or from the terminal (`en0` for Wi-Fi, `en1` for Ethernet):

```bash
sudo networksetup -setdnsservers Wi-Fi 127.0.0.1 1.1.1.1
```

## 7. Backups

```bash
sudo cp /usr/local/var/privatedns/privatedns.db \
        /Users/Shared/Backups/privatedns-$(date -u +%Y%m%dT%H%M%SZ).db
```

## Uninstall

```bash
sudo deploy/macos/uninstall.sh          # keep data
sudo deploy/macos/uninstall.sh --purge  # wipe everything
```

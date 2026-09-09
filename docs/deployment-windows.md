# Deploying privatedns on Windows

Runs as a Windows Service via the built-in Service Control Manager. The
binary registers itself — no NSSM, no external service wrapper.

## 1. Prerequisites

- Windows 10, 11, or Windows Server 2019+
- Administrator PowerShell to install as a service
- Port 53 free — see §3 if it's not

## 2. Install

Grab the release zip for your architecture (amd64 or arm64), extract it, and
in an **elevated PowerShell**:

```powershell
Expand-Archive privatedns_*_windows_amd64.zip -DestinationPath C:\PrivateDNS
cd C:\PrivateDNS\privatedns_*_windows_amd64

# (optional) set any config you want the service to inherit
$env:PRIVATEDNS_DATA_DIR      = "C:\ProgramData\privatedns\data"
$env:PRIVATEDNS_PRIVATE_TLD   = "myworld"
$env:PRIVATEDNS_ADMIN_EMAIL   = "admin@local"
# (do not set PRIVATEDNS_ADMIN_PASSWORD — let the binary generate one)

.\privatedns.exe service install
.\privatedns.exe service start
```

The `service install` step captures every `PRIVATEDNS_*` variable currently
set in your shell and writes them to `%ProgramData%\privatedns\service.env`.
The service reads this file at startup, so you can edit the file and restart
the service to change config.

The bootstrap admin password is printed in the log file
(`%PRIVATEDNS_DATA_DIR%\logs\privatedns.log`) or via Event Viewer.

## 3. Fix port 53 conflict

Windows sometimes reserves UDP port 5353 for mDNS/Bonjour. Port 53 itself is
usually free unless another DNS server is installed.

Check what's using port 53:

```powershell
Get-NetUDPEndpoint -LocalPort 53 -ErrorAction SilentlyContinue |
  Select-Object -Property LocalAddress, LocalPort, @{n="PID";e={$_.OwningProcess}}
Get-NetTCPConnection -LocalPort 53 -ErrorAction SilentlyContinue |
  Select-Object -Property LocalAddress, LocalPort, State, OwningProcess
```

If nothing is listening but `privatedns` still can't bind, check for
reserved port ranges (common with Hyper-V):

```powershell
netsh int ipv4 show excludedportrange protocol=udp
netsh int ipv4 show excludedportrange protocol=tcp
```

If port 53 falls in an excluded range, either use a different port
(`PRIVATEDNS_DNS_ADDR=":15353"`) or disable Hyper-V's dynamic ports.

## 4. Firewall

Allow inbound DNS + dashboard, restricted to your LAN or VPN subnet:

```powershell
New-NetFirewallRule -DisplayName "privatedns UDP" -Direction Inbound `
  -Protocol UDP -LocalPort 53 -RemoteAddress 10.10.0.0/16 -Action Allow
New-NetFirewallRule -DisplayName "privatedns TCP" -Direction Inbound `
  -Protocol TCP -LocalPort 53 -RemoteAddress 10.10.0.0/16 -Action Allow
New-NetFirewallRule -DisplayName "privatedns dashboard" -Direction Inbound `
  -Protocol TCP -LocalPort 8080 -RemoteAddress 10.10.0.0/16 -Action Allow
```

Adjust `-RemoteAddress` to match your network.

## 5. Verify

```powershell
.\privatedns.exe service status
Get-Service privatedns

# From another shell (or wait, it's a bit)
Resolve-DnsName -Server 127.0.0.1 -Type A www.example.myworld -ErrorAction SilentlyContinue
```

`Resolve-DnsName` doesn't accept custom ports, but the default port 53 works.
For a specific port, use the `dig.exe` helper bundled at the repo root or
[nslookup with `server 127.0.0.1` interactive mode].

## 6. Manage the service

```powershell
.\privatedns.exe service status
.\privatedns.exe service stop
.\privatedns.exe service start
.\privatedns.exe service uninstall
```

or from Services.msc — search for "privatedns".

## 7. Logs

Because Windows services have no console, logs go to a file:

```
%PRIVATEDNS_DATA_DIR%\logs\privatedns.log
```

For real-time tailing:

```powershell
Get-Content "$env:PRIVATEDNS_DATA_DIR\logs\privatedns.log" -Wait -Tail 50
```

The service also registers with Event Log — check "Application" for entries
sourced by `privatedns`.

## 8. Backups

```powershell
Copy-Item "$env:PRIVATEDNS_DATA_DIR\privatedns.db" `
  "C:\Backups\privatedns-$(Get-Date -Format 'yyyyMMdd-HHmmss').db"
```

## Uninstall

```powershell
.\privatedns.exe service uninstall
# Optionally:
Remove-Item -Recurse "$env:PRIVATEDNS_DATA_DIR", "$env:ProgramData\privatedns"
```

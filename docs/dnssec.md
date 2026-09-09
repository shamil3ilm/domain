# DNSSEC

PowerDNS supports DNSSEC natively. Signing a public zone is straightforward:
generate a KSK, publish the DS at the parent, and PowerDNS handles the rest.

Signing a **private** TLD is more work because there's no publicly-anchored
parent to hold the DS record. You must distribute the trust anchor to your
recursors out-of-band.

## Signing a zone

```bash
# Add DNSSEC to example.myworld (creates KSK+ZSK, starts signing).
docker compose exec pdns-auth pdnsutil secure-zone example.myworld

# Inspect keys.
docker compose exec pdns-auth pdnsutil show-zone example.myworld

# Export the DS record (needed if this zone is delegated by a signed parent).
docker compose exec pdns-auth pdnsutil export-zone-ds example.myworld
```

For a **public** zone, you'd upload the DS record to your registrar. For a
private zone under `.myworld`, you can either:

- Publish the DS in the private-root zone (`.myworld` itself) if you also
  signed that, or
- Skip the DS and distribute the DNSKEY of the zone as a trust anchor on each
  recursor.

## Trust anchor for the private root

If you signed `.myworld` itself, export its DNSKEY:

```bash
docker compose exec pdns-auth pdnsutil export-zone-key myworld <key-id>
```

...and add it to `unbound.conf` on each recursor:

```
server:
    trust-anchor: "myworld. IN DNSKEY 257 3 13 <base64...>"
```

Also remove the `domain-insecure: "${PRIVATE_TLD}"` line so validation
happens.

Restart the recursor.

## Key rotation

Rolling a ZSK (routine, low-risk):

```bash
docker compose exec pdns-auth pdnsutil activate-zone-key <zone> <new-zsk-id>
docker compose exec pdns-auth pdnsutil deactivate-zone-key <zone> <old-zsk-id>
# wait one TTL, then:
docker compose exec pdns-auth pdnsutil remove-zone-key <zone> <old-zsk-id>
```

Rolling a KSK (double-DS method for public zones; for private, you have to
push the new trust-anchor to recursors before removing the old key). Follow
the PowerDNS docs for the exact sequence.

## Monitoring

DNSSEC failures usually manifest as `SERVFAIL` from the recursor. Grafana's
"privatedns / Overview" dashboard includes an "auth SERVFAIL rate" panel —
watch it after key rotations.

## Recommendation

For private-only zones, DNSSEC adds operational cost (trust-anchor
distribution) without a lot of security benefit — you already control both
ends of the resolution chain. Turn it on if you have a specific requirement
(e.g. auditor, DANE for internal mail); skip it otherwise.

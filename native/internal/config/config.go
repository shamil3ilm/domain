// Package config parses environment variables into a validated Config.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DataDir       string
	PrivateTLD    string
	DNSAddr       string
	APIAddr       string
	Upstreams     []string
	AdminEmail    string
	AdminPassword string // empty means "generate one on first run"

	// AllowQueryFrom controls who may send DNS queries at all. Empty = allow
	// everyone (safe when the server is authoritative-only).
	AllowQueryFrom []string

	// AllowRecursionFrom controls who may cause the server to look up names
	// outside its own zones. Empty = loopback only. NEVER leave this open to
	// the Internet — open recursors are abusable amplifiers.
	AllowRecursionFrom []string

	// DNS query rate limiting (per source IP, token-bucket). Values <=0
	// disable limiting; loopback + configured exempt CIDRs bypass regardless.
	DNSRateLimitPerSec float64
	DNSRateLimitBurst  int
	DNSRateLimitExempt []string

	// Login lockout: N failed attempts in a rolling window per (email, ip)
	// short-circuits the login handler with 429 + Retry-After. Setting max
	// to 0 disables the lockout entirely.
	LoginMaxAttempts   int
	LoginLockoutWindow time.Duration

	// APITLSMode controls how the management API terminates TLS:
	//   "off"  — plain HTTP (fine when bound to loopback).
	//   "auto" — self-signed cert generated + persisted in DataDir/tls.
	//   "cert" — read the cert + key from APITLSCert / APITLSKey.
	APITLSMode  string
	APITLSCert  string
	APITLSKey   string
	APITLSHosts []string

	LogLevel string

	// Populated by main after loading secret file.
	JWTSecret string
}

func Load() (*Config, error) {
	c := &Config{
		DataDir:       env("PRIVATEDNS_DATA_DIR", "./data"),
		PrivateTLD:    strings.TrimSuffix(strings.ToLower(env("PRIVATEDNS_PRIVATE_TLD", "myworld")), "."),
		DNSAddr:       env("PRIVATEDNS_DNS_ADDR", ":53"),
		APIAddr:       env("PRIVATEDNS_API_ADDR", ":8080"),
		AdminEmail:    strings.ToLower(env("PRIVATEDNS_ADMIN_EMAIL", "admin@local")),
		AdminPassword: os.Getenv("PRIVATEDNS_ADMIN_PASSWORD"),
		LogLevel:      env("PRIVATEDNS_LOG_LEVEL", "info"),
	}

	up := env("PRIVATEDNS_UPSTREAMS", "1.1.1.1:53,9.9.9.9:53")
	for _, s := range strings.Split(up, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, ":") {
			s = s + ":53"
		}
		c.Upstreams = append(c.Upstreams, s)
	}

	// AllowQueryFrom: PRIVATEDNS_ALLOW_QUERY_FROM (or legacy PRIVATEDNS_ALLOW_FROM).
	c.AllowQueryFrom = parseCIDRList(env2("PRIVATEDNS_ALLOW_QUERY_FROM", "PRIVATEDNS_ALLOW_FROM"))

	// AllowRecursionFrom: default to loopback only. If the operator wants
	// recursion for LAN/VPN clients they must opt in explicitly.
	if v := os.Getenv("PRIVATEDNS_ALLOW_RECURSION_FROM"); v != "" {
		c.AllowRecursionFrom = parseCIDRList(v)
	} else {
		c.AllowRecursionFrom = []string{"127.0.0.0/8", "::1/128"}
	}

	// DNS rate limiting. Defaults: 20 QPS steady-state, 40 burst, loopback exempt.
	rate, err := parseFloatDefault("PRIVATEDNS_DNS_RATE_LIMIT_PER_SEC", 20)
	if err != nil {
		return nil, err
	}
	burst, err := parseIntDefault("PRIVATEDNS_DNS_RATE_LIMIT_BURST", 40)
	if err != nil {
		return nil, err
	}
	c.DNSRateLimitPerSec = rate
	c.DNSRateLimitBurst = burst
	if v := os.Getenv("PRIVATEDNS_DNS_RATE_LIMIT_EXEMPT_CIDR"); v != "" {
		c.DNSRateLimitExempt = parseCIDRList(v)
	} else {
		c.DNSRateLimitExempt = []string{"127.0.0.0/8", "::1/128"}
	}

	// Login lockout. Defaults: 5 failures per 15 minutes per (email, IP).
	maxAttempts, err := parseIntDefault("PRIVATEDNS_LOGIN_MAX_ATTEMPTS", 5)
	if err != nil {
		return nil, err
	}
	c.LoginMaxAttempts = maxAttempts
	lockoutWindow, err := parseDurationDefault("PRIVATEDNS_LOGIN_LOCKOUT_WINDOW", 15*time.Minute)
	if err != nil {
		return nil, err
	}
	c.LoginLockoutWindow = lockoutWindow

	// TLS on the management API. Default off so an operator running with
	// the API bound to loopback (the recommended posture) doesn't have to
	// deal with self-signed certs. Anyone binding to a real interface should
	// flip this to "auto" or "cert".
	c.APITLSMode = strings.ToLower(env("PRIVATEDNS_API_TLS", "off"))
	switch c.APITLSMode {
	case "off", "auto", "cert":
	default:
		return nil, fmt.Errorf("PRIVATEDNS_API_TLS: unknown mode %q (want off|auto|cert)", c.APITLSMode)
	}
	c.APITLSCert = os.Getenv("PRIVATEDNS_API_TLS_CERT")
	c.APITLSKey = os.Getenv("PRIVATEDNS_API_TLS_KEY")
	if c.APITLSMode == "cert" && (c.APITLSCert == "" || c.APITLSKey == "") {
		return nil, fmt.Errorf("PRIVATEDNS_API_TLS=cert requires PRIVATEDNS_API_TLS_CERT and _KEY")
	}
	if hosts := os.Getenv("PRIVATEDNS_API_TLS_HOSTS"); hosts != "" {
		for _, h := range strings.Split(hosts, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				c.APITLSHosts = append(c.APITLSHosts, h)
			}
		}
	}

	// If no bootstrap password supplied, generate one — main prints it once.
	if c.AdminPassword == "" {
		b := make([]byte, 18)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate admin password: %w", err)
		}
		c.AdminPassword = base64.RawURLEncoding.EncodeToString(b)
	}

	return c, nil
}

func parseCIDRList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func env2(primary, fallback string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(fallback)
}

func parseFloatDefault(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid float %q: %w", key, v, err)
	}
	return n, nil
}

func parseIntDefault(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid int %q: %w", key, v, err)
	}
	return n, nil
}

func parseDurationDefault(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, v, err)
	}
	return d, nil
}

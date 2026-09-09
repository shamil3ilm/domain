// Package config parses environment variables into a validated Config.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
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

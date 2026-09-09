// Package config parses environment variables into a validated Config.
package config

import (
	"crypto/rand"
	"encoding/base64"
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
	AllowFrom     []string
	LogLevel      string

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

	if af := os.Getenv("PRIVATEDNS_ALLOW_FROM"); af != "" {
		for _, s := range strings.Split(af, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				c.AllowFrom = append(c.AllowFrom, s)
			}
		}
	}

	// If no bootstrap password supplied, generate one — main prints it once.
	if c.AdminPassword == "" {
		b := make([]byte, 18)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		c.AdminPassword = base64.RawURLEncoding.EncodeToString(b)
	}

	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

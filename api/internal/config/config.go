// Package config parses environment variables into a validated Config.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listen    string
	LogLevel  string
	JWTSecret string
	RateLimit int

	PGHost, PGPort, PGDB, PGUser, PGPass string

	PDNSURL       string
	PDNSKey       string
	PDNSDefaultNS string
	PDNSPublicIP  string

	AdminEmail    string
	AdminPassword string

	PrivateTLD     string
	BootstrapZones []string
}

func Load() (*Config, error) {
	c := &Config{
		Listen:         getenv("API_LISTEN", ":8080"),
		LogLevel:       getenv("API_LOG_LEVEL", "info"),
		JWTSecret:      os.Getenv("API_JWT_SECRET"),
		PGHost:         os.Getenv("PGHOST"),
		PGPort:         getenv("PGPORT", "5432"),
		PGDB:           os.Getenv("PGDATABASE"),
		PGUser:         os.Getenv("PGUSER"),
		PGPass:         os.Getenv("PGPASSWORD"),
		PDNSURL:        os.Getenv("PDNS_API_URL"),
		PDNSKey:        os.Getenv("PDNS_API_KEY"),
		PDNSDefaultNS:  os.Getenv("PDNS_DEFAULT_NS"),
		PDNSPublicIP:   os.Getenv("PDNS_PUBLIC_IP"),
		AdminEmail:     os.Getenv("ADMIN_EMAIL"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		PrivateTLD:     os.Getenv("PRIVATE_TLD"),
	}

	if b := os.Getenv("BOOTSTRAP_ZONES"); b != "" {
		for _, z := range strings.Split(b, ",") {
			z = strings.TrimSpace(z)
			if z != "" {
				c.BootstrapZones = append(c.BootstrapZones, z)
			}
		}
	}

	if rl := os.Getenv("API_RATE_LIMIT"); rl != "" {
		n, err := strconv.Atoi(rl)
		if err != nil {
			return nil, fmt.Errorf("API_RATE_LIMIT: %w", err)
		}
		c.RateLimit = n
	} else {
		c.RateLimit = 120
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("API_JWT_SECRET must be at least 32 chars")
	}
	if c.PGHost == "" || c.PGDB == "" || c.PGUser == "" || c.PGPass == "" {
		return fmt.Errorf("PGHOST/PGDATABASE/PGUSER/PGPASSWORD required")
	}
	if c.PDNSURL == "" || c.PDNSKey == "" {
		return fmt.Errorf("PDNS_API_URL and PDNS_API_KEY required")
	}
	if c.PrivateTLD == "" {
		return fmt.Errorf("PRIVATE_TLD required")
	}
	if c.AdminEmail == "" || c.AdminPassword == "" {
		return fmt.Errorf("ADMIN_EMAIL and ADMIN_PASSWORD required for first-boot bootstrap")
	}
	return nil
}

func (c *Config) DatabaseDSN() string {
	// search_path is a Postgres session parameter, not a libpq connection
	// param — pass it via the "options" query key, which libpq forwards as
	// startup packet options.
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable&options=-csearch_path%%3Dmgmt%%2Cpublic",
		c.PGUser, c.PGPass, c.PGHost, c.PGPort, c.PGDB)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Package bootstrap runs once at API startup. It creates the initial admin
// user, seeds zone metadata, and provisions the private-root and any zones
// listed in BOOTSTRAP_ZONES if they don't already exist.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/privatedns/api/internal/auth"
	"github.com/privatedns/api/internal/config"
	"github.com/privatedns/api/internal/db"
	"github.com/privatedns/api/internal/pdns"
)

func Run(ctx context.Context, pool *db.Pool, pd *pdns.Client, cfg *config.Config) error {
	if err := ensureAdmin(ctx, pool, cfg); err != nil {
		return fmt.Errorf("ensure admin: %w", err)
	}
	if err := ensureRootZone(ctx, pd, cfg); err != nil {
		return fmt.Errorf("ensure root zone: %w", err)
	}
	for _, z := range cfg.BootstrapZones {
		if err := ensureZone(ctx, pool, pd, cfg, z); err != nil {
			slog.Warn("bootstrap.zone", "zone", z, "err", err)
		}
	}
	return nil
}

func ensureAdmin(ctx context.Context, pool *db.Pool, cfg *config.Config) error {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mgmt.users WHERE role = 'admin'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO mgmt.users (email, password_hash, role) VALUES ($1, $2, 'admin')
	`, strings.ToLower(cfg.AdminEmail), hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	slog.Info("bootstrap.admin_created", "email", cfg.AdminEmail)
	return nil
}

// ensureRootZone creates the private TLD zone if it doesn't exist and puts
// glue A records for the nameserver.
func ensureRootZone(ctx context.Context, pd *pdns.Client, cfg *config.Config) error {
	tld := strings.TrimSuffix(cfg.PrivateTLD, ".") + "."
	_, err := pd.GetZone(ctx, tld)
	if err == nil {
		return nil
	}
	if pe, ok := err.(*pdns.APIError); !ok || !pe.NotFound() {
		return err
	}
	_, err = pd.CreateZone(ctx, pdns.Zone{
		Name:        tld,
		Kind:        "Native",
		Nameservers: []string{cfg.PDNSDefaultNS},
	})
	if err != nil {
		return err
	}

	// Glue A: ns1.<tld>. -> DNS_PUBLIC_IP
	nsFQDN := cfg.PDNSDefaultNS
	if cfg.PDNSPublicIP != "" {
		if err := pd.PatchRRsets(ctx, tld, []pdns.RRset{{
			Name:       nsFQDN,
			Type:       "A",
			TTL:        3600,
			ChangeType: "REPLACE",
			Records:    []pdns.Record{{Content: cfg.PDNSPublicIP}},
		}}); err != nil {
			return err
		}
	}
	slog.Info("bootstrap.root_zone_created", "zone", tld)
	return nil
}

func ensureZone(ctx context.Context, pool *db.Pool, pd *pdns.Client, cfg *config.Config, name string) error {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".") + "."
	_, err := pd.GetZone(ctx, name)
	if err == nil {
		return nil
	}
	if pe, ok := err.(*pdns.APIError); !ok || !pe.NotFound() {
		return err
	}
	_, err = pd.CreateZone(ctx, pdns.Zone{
		Name:        name,
		Kind:        "Native",
		Nameservers: []string{cfg.PDNSDefaultNS},
	})
	if err != nil {
		return err
	}
	// Record metadata.
	_, _ = pool.Exec(ctx, `
		INSERT INTO mgmt.zone_meta (zone_name, is_private, description)
		VALUES ($1, TRUE, 'bootstrap')
		ON CONFLICT (zone_name) DO NOTHING
	`, strings.TrimSuffix(name, "."))

	// If this is a subzone of the private TLD, add a delegation record in the
	// parent (NS record). PowerDNS handles this automatically only if both
	// zones live in the same instance and NS records are set up.
	tld := strings.TrimSuffix(cfg.PrivateTLD, ".")
	sub := strings.TrimSuffix(name, ".")
	if sub != tld && strings.HasSuffix(sub, "."+tld) {
		_ = pd.PatchRRsets(ctx, tld+".", []pdns.RRset{{
			Name:       name,
			Type:       "NS",
			TTL:        3600,
			ChangeType: "REPLACE",
			Records:    []pdns.Record{{Content: cfg.PDNSDefaultNS}},
		}})
	}
	slog.Info("bootstrap.zone_created", "zone", name)
	return nil
}
